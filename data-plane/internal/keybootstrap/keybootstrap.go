// Package keybootstrap performs the CONTROLLED, DEPLOYMENT-TIME bootstrap of appliance-local HMAC
// secret material and its database lifecycle metadata for the durable throttle (D4) and keyed-HMAC
// OTP (D7).
//
// It is intentionally separate from the service runtime: scd only ever LOADS existing keys
// (localkeys.LoadExistingKey / LoadGenerationKeys) and never creates them. Key creation and the
// otp_hmac_key_generations metadata row are established here, once, by an operator/deploy step using
// a privileged (migration/operational) role — never by svc_scd at runtime.
//
// Fail-closed invariants enforced here:
//   - DB metadata present but its key file absent  => fail (never fabricate a key for a known gen);
//   - key file present but DB has no generation     => controlled reconcile ONLY for a lone gen 1;
//     any other shape fails (ambiguous);
//   - more than one active generation               => fail;
//   - a generation referenced by an unexpired OTP but missing its key => fail;
//   - rotation is an explicit operational action, never an implicit side effect of bootstrap.
//
// Ordering guarantees a safe rollback: the key file is created FIRST and never deleted, then the DB
// row is written inside a transaction that only commits after an in-transaction single-active check.
// A failed bootstrap therefore leaves the key file (harmless, reused on retry) and makes no partial
// DB change.
package keybootstrap

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/stayconnect/enterprise/data-plane/internal/localkeys"
)

// OTPPrefix is the generation-key file prefix (files are "<prefix>_<gen>.key").
const OTPPrefix = "otp_hmac"

// DB is the minimal database surface keybootstrap needs (satisfied by *pgxpool.Pool).
type DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// OTPResult reports what the bootstrap observed/did (no secret material).
type OTPResult struct {
	ActiveGeneration int
	Referenced       []int
	KeyGenerations   []int
	CreatedKey       bool // a gen-1 key file was created this run
	InsertedRow      bool // a generation metadata row was inserted this run
}

// BootstrapThrottleKey creates the durable-throttle key file if absent (deployment). It never
// overwrites an existing key and is safe to run repeatedly.
func BootstrapThrottleKey(path string) error {
	_, err := localkeys.CreateKeyIfAbsent(path)
	return err
}

// BootstrapOTP establishes OTP key generation 1 (or reconciles/validates an existing lifecycle) and
// proves that key material and DB metadata agree before scd is allowed to start. It is idempotent.
func BootstrapOTP(ctx context.Context, db DB, keyDir string) (OTPResult, error) {
	var res OTPResult

	dbGens, err := allGenerations(ctx, db)
	if err != nil {
		return res, err
	}
	active, err := activeGenerations(ctx, db)
	if err != nil {
		return res, err
	}
	if len(active) > 1 {
		return res, fmt.Errorf("keybootstrap: %d active OTP generations (want exactly 1); refusing (rotation is explicit)", len(active))
	}
	keys, err := localkeys.LoadGenerationKeys(keyDir, OTPPrefix)
	if err != nil {
		return res, err
	}

	switch {
	case len(dbGens) == 0 && len(keys) == 0:
		// First-time bootstrap of generation 1: create the key file FIRST, then insert the row.
		if _, err := localkeys.EnsureGeneration(keyDir, OTPPrefix, 1); err != nil {
			return res, err
		}
		res.CreatedKey = true
		if err := insertActiveGeneration(ctx, db, 1, "phase1b bootstrap"); err != nil {
			return res, err
		}
		res.InsertedRow = true

	case len(dbGens) == 0 && len(keys) > 0:
		// Key present, DB empty: reconcile ONLY the unambiguous lone-generation-1 case.
		if len(keys) == 1 {
			if _, ok := keys[1]; ok {
				if err := insertActiveGeneration(ctx, db, 1, "phase1b reconcile from key"); err != nil {
					return res, err
				}
				res.InsertedRow = true
				break
			}
		}
		return res, fmt.Errorf("keybootstrap: key files %v present but no DB generation; refusing ambiguous reconcile", localkeys.SortedGenerations(keys))

	default:
		// DB already has generations. Do NOT create any key here — a known generation whose key is
		// missing must fail (handled by the ValidateOTPGenerations check below), never be fabricated.
	}

	// Re-read authoritative state after any controlled change.
	active, err = activeGenerations(ctx, db)
	if err != nil {
		return res, err
	}
	if len(active) != 1 {
		return res, fmt.Errorf("keybootstrap: expected exactly 1 active OTP generation, found %d", len(active))
	}
	res.ActiveGeneration = active[0]

	keys, err = localkeys.LoadGenerationKeys(keyDir, OTPPrefix)
	if err != nil {
		return res, err
	}
	res.KeyGenerations = localkeys.SortedGenerations(keys)

	referenced, err := referencedGenerations(ctx, db)
	if err != nil {
		return res, err
	}
	res.Referenced = referenced

	// Fail closed if the active generation, or any generation still referenced by an unexpired OTP,
	// lacks key material.
	if err := localkeys.ValidateOTPGenerations(keys, res.ActiveGeneration, referenced); err != nil {
		return res, err
	}
	return res, nil
}

func allGenerations(ctx context.Context, db DB) ([]int, error) {
	return scanInts(ctx, db, `SELECT generation FROM public.otp_hmac_key_generations ORDER BY generation`)
}

func activeGenerations(ctx context.Context, db DB) ([]int, error) {
	return scanInts(ctx, db, `SELECT generation FROM public.otp_hmac_key_generations WHERE active ORDER BY generation`)
}

// referencedGenerations returns the distinct non-NULL key generations pinned by OTPs that are not yet
// expired — their key material must remain present or those OTPs could never verify.
func referencedGenerations(ctx context.Context, db DB) ([]int, error) {
	return scanInts(ctx, db,
		`SELECT DISTINCT otp_key_generation FROM public.auth_otps
		  WHERE otp_key_generation IS NOT NULL AND expires_at > now()
		  ORDER BY otp_key_generation`)
}

func scanInts(ctx context.Context, db DB, sql string) ([]int, error) {
	rows, err := db.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// insertActiveGeneration writes one active generation row inside a transaction that commits only after
// verifying (in-tx) that exactly one generation is active — so a concurrent bootstrap can never leave
// two active. The unique partial index otp_hmac_key_generations_one_active is the hard backstop.
func insertActiveGeneration(ctx context.Context, db DB, gen int, note string) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx,
		`INSERT INTO public.otp_hmac_key_generations (generation, active, note)
		 VALUES ($1, true, $2)
		 ON CONFLICT (generation) DO NOTHING`, gen, note); err != nil {
		return err
	}
	var activeCount int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM public.otp_hmac_key_generations WHERE active`).Scan(&activeCount); err != nil {
		return err
	}
	if activeCount != 1 {
		return fmt.Errorf("keybootstrap: refusing commit — %d active generations after insert", activeCount)
	}
	return tx.Commit(ctx)
}
