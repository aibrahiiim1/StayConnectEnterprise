package pmsresolve

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Probe asks ONE interface for its determinate verdict on the guest evidence. An implementation must never
// block indefinitely: the fan-out bounds it, and a probe that does not answer in time is UNAVAILABLE — an
// indeterminate verdict, never a NO_MATCH, because "we could not ask" and "we asked and it said no" are
// different facts and only the second one is safe to act on.
type Probe interface {
	Probe(ctx context.Context, ifaceID string) (CandidateOutcome, string, error)
}

// ProbeFunc adapts a function to Probe. The second return value is the matched Stay id (only meaningful for a
// VERIFIED verdict).
type ProbeFunc func(ctx context.Context, ifaceID string) (CandidateOutcome, string, error)

func (f ProbeFunc) Probe(ctx context.Context, ifaceID string) (CandidateOutcome, string, error) {
	return f(ctx, ifaceID)
}

// ErrNoRequestID — an idempotent resolution needs a caller-supplied request id. Without one, a retry would
// record a second resolution for the same guest attempt, so the resolver fails closed rather than duplicating.
var ErrNoRequestID = errors.New("pmsresolve: resolution_request_id is required")

// Resolver gathers the COMPLETE candidate vector for a guest network and records the strict outcome.
type Resolver struct {
	pool *pgxpool.Pool
	// MaxCandidates bounds the mapped-interface vector (0 = unbounded). Over the cap is INDETERMINATE.
	MaxCandidates int
	// ProbeTimeout bounds EACH candidate probe. Exceeding it is UNAVAILABLE for that candidate only.
	ProbeTimeout time.Duration
}

func NewResolver(pool *pgxpool.Pool) *Resolver {
	return &Resolver{pool: pool, ProbeTimeout: 5 * time.Second}
}

// Result is a recorded resolution.
type Result struct {
	Outcome
	Stay string
	// Replayed is true when this request id had already been resolved and the STORED outcome was returned
	// unchanged (the probes are not re-run and no second row is written).
	Replayed bool
	// Candidates is the complete vector the decision was made on, in stable interface order.
	Candidates []Candidate
}

// Resolve gathers every mapped interface's verdict CONCURRENTLY, waits for the whole vector (a slow VERIFIED
// must still beat a fast NO_MATCH), applies the strict decision, and records it IDEMPOTENTLY against the
// caller's request id. Concurrent retries of the same request id converge on one stored resolution.
func (r *Resolver) Resolve(ctx context.Context, tenant, site, guestNetwork, requestID string, p Probe) (Result, error) {
	var res Result
	if requestID == "" {
		return res, ErrNoRequestID
	}
	// (1) already resolved? replay the stored outcome without re-probing anything.
	if prev, ok, err := r.load(ctx, tenant, site, requestID); err != nil {
		return res, err
	} else if ok {
		prev.Replayed = true
		return prev, nil
	}

	// (2) the mapped vector — from the TRUSTED guest network mapping, never a client hint. Ordered so the
	// recorded vector is stable and comparable across retries.
	ifaces, err := r.mapped(ctx, tenant, site, guestNetwork)
	if err != nil {
		return res, err
	}

	// (3) fan out. Every candidate is probed; the decision waits for ALL of them.
	cands := make([]Candidate, len(ifaces))
	stays := make([]string, len(ifaces))
	var wg sync.WaitGroup
	for i, id := range ifaces {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			pctx := ctx
			if r.ProbeTimeout > 0 {
				var cancel context.CancelFunc
				pctx, cancel = context.WithTimeout(ctx, r.ProbeTimeout)
				defer cancel()
			}
			out, stay, err := p.Probe(pctx, id)
			if err != nil || out == "" {
				// a failed / timed-out / empty probe is INDETERMINATE, never a determinate NO_MATCH
				out, stay = Unavailable, ""
			}
			cands[i] = Candidate{InterfaceID: id, Outcome: out}
			if out == Verified {
				stays[i] = stay
			}
		}(i, id)
	}
	wg.Wait()

	o := Resolve(cands, r.MaxCandidates)
	stay := ""
	if o.Resolution == ResVerified {
		for i := range cands {
			if cands[i].InterfaceID == o.InterfaceID {
				stay = stays[i]
			}
		}
		// a VERIFIED verdict with no Stay identity proves nothing — fail closed rather than record a success
		// that cannot be acted on.
		if stay == "" {
			o = Outcome{Resolution: ResIndeterminate, Reason: "VERIFIED_WITHOUT_STAY"}
		}
	}
	res.Outcome, res.Stay, res.Candidates = o, stay, cands
	if o.Resolution != ResVerified {
		res.Stay = ""
	}

	// (4) record idempotently. A concurrent racer that won the unique index means this request was already
	// resolved: return the STORED outcome so every caller of one request id sees the same answer.
	stored, err := r.store(ctx, tenant, site, guestNetwork, requestID, res)
	if err != nil {
		return Result{}, err
	}
	if stored != nil {
		stored.Candidates = cands
		stored.Replayed = true
		return *stored, nil
	}
	return res, nil
}

func (r *Resolver) mapped(ctx context.Context, tenant, site, guestNetwork string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT m.pms_interface_id::text
		FROM iam_v2.guest_network_pms_map m
		JOIN iam_v2.pms_interfaces pi ON pi.tenant_id=m.tenant_id AND pi.site_id=m.site_id AND pi.id=m.pms_interface_id
		WHERE m.tenant_id=$1 AND m.site_id=$2 AND m.guest_network_id=$3 AND pi.lifecycle_state='ACTIVE'
		ORDER BY m.pms_interface_id`, tenant, site, guestNetwork)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func (r *Resolver) load(ctx context.Context, tenant, site, requestID string) (Result, bool, error) {
	var res Result
	var code string
	var stay *string
	err := r.pool.QueryRow(ctx, `SELECT outcome_code, resolved_stay_id::text FROM iam_v2.auth_resolutions
		WHERE tenant_id=$1 AND site_id=$2 AND resolution_request_id=$3`, tenant, site, requestID).Scan(&code, &stay)
	if errors.Is(err, pgx.ErrNoRows) {
		return res, false, nil
	}
	if err != nil {
		return res, false, err
	}
	res.Outcome = Outcome{Resolution: Resolution(code), Reason: "REPLAYED"}
	if stay != nil {
		res.Stay = *stay
	}
	return res, true, nil
}

// store writes the resolution. It returns a non-nil Result when the row already existed (a concurrent racer
// resolved the same request id first) — the STORED outcome, which is the one every caller must see.
func (r *Resolver) store(ctx context.Context, tenant, site, guestNetwork, requestID string, res Result) (*Result, error) {
	var stayArg any
	if res.Stay != "" {
		stayArg = res.Stay
	}
	ct, err := r.pool.Exec(ctx, `INSERT INTO iam_v2.auth_resolutions
		(tenant_id, site_id, guest_network_id, resolved_stay_id, outcome_code, resolution_request_id)
		VALUES ($1,$2,$3,$4::uuid,$5,$6)
		ON CONFLICT (tenant_id, site_id, resolution_request_id) WHERE resolution_request_id IS NOT NULL
		DO NOTHING`,
		tenant, site, guestNetwork, stayArg, string(res.Resolution), requestID)
	if err != nil {
		return nil, err
	}
	if ct.RowsAffected() == 1 {
		return nil, nil
	}
	prev, ok, err := r.load(ctx, tenant, site, requestID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("pmsresolve: resolution conflicted but could not be read back")
	}
	return &prev, nil
}
