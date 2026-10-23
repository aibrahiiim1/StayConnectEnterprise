// Package keybootstrap performs the deployment-time bootstrap of the appliance-local HMAC keys and the
// otp_hmac_key_generations metadata row; scd only loads existing keys, never creates them.
//
// Fails closed: metadata without its key file, ambiguous key/DB shapes, several active generations, or an
// unexpired OTP generation missing its key. Rotation is never an implicit side effect. The key file is created
// first and never deleted, then the row commits in a transaction after a single-active check, so a failed
// bootstrap leaves only a harmless key file.
package keybootstrap

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/stayconnect/enterprise/data-plane/internal/localkeys"
)

// OTPPrefix is the generation-key file prefix (files are "<prefix>_<gen>.key").
const OTPPrefix = "otp_hmac"

// VoucherHMACKeyFile is the appliance-local blind-index key for iam_v2.vouchers (code_hmac). scd refuses to
// start without it, so it is minted here, never overwritten and never by the daemon.
const VoucherHMACKeyFile = "voucher_hmac.key"

// BootstrapVoucherHMACKey creates the voucher key file if absent. It never overwrites: that would orphan every
// indexed voucher.
func BootstrapVoucherHMACKey(path string) error {
	_, err := localkeys.CreateKeyIfAbsent(path)
	return err
}

type DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

type OTPResult struct {
	ActiveGeneration int
	Referenced       []int
	KeyGenerations   []int
	CreatedKey       bool
	InsertedRow      bool
}

// BootstrapThrottleKey creates the durable-throttle key file if absent; it never overwrites an existing key.
func BootstrapThrottleKey(path string) error {
	_, err := localkeys.CreateKeyIfAbsent(path)
	return err
}

// BootstrapOTP establishes OTP key generation 1 or validates the existing lifecycle so key material and DB
// metadata agree; idempotent.
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
		// Create the key file first, then insert the row.
		if _, err := localkeys.EnsureGeneration(keyDir, OTPPrefix, 1); err != nil {
			return res, err
		}
		res.CreatedKey = true
		if err := insertActiveGeneration(ctx, db, 1, "phase1b bootstrap"); err != nil {
			return res, err
		}
		res.InsertedRow = true

	case len(dbGens) == 0 && len(keys) > 0:
		// Key present, DB empty: reconcile only the lone generation-1 case.
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
		// Never create a key for a known generation; a missing one must fail in the validation below.
	}

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

// referencedGenerations returns the key generations pinned by unexpired OTPs; their keys must stay present.
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

// insertActiveGeneration commits one active row only after verifying in-tx that exactly one generation is active;
// the unique partial index otp_hmac_key_generations_one_active is the backstop.
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
