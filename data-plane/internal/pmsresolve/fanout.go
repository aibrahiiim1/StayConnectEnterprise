package pmsresolve

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

// Probe asks one interface for its verdict. A probe that doesn't answer in time is UNAVAILABLE, never
// NO_MATCH: "could not ask" differs from "asked and it said no", and only the second is safe to act on.
type Probe interface {
	Probe(ctx context.Context, ifaceID string) (CandidateOutcome, string, error)
}

// ProbeFunc's second return value is the matched Stay id, meaningful only for VERIFIED.
type ProbeFunc func(ctx context.Context, ifaceID string) (CandidateOutcome, string, error)

func (f ProbeFunc) Probe(ctx context.Context, ifaceID string) (CandidateOutcome, string, error) {
	return f(ctx, ifaceID)
}

// Without a request id a retry would record a second resolution, so the resolver fails closed.
var ErrNoRequestID = errors.New("pmsresolve: resolution_request_id is required")

type Resolver struct {
	pool *pgxpool.Pool
	// MaxCandidates bounds the interface vector (0 = unbounded); over the cap is INDETERMINATE.
	MaxCandidates int
	// ProbeTimeout bounds each probe; exceeding it is UNAVAILABLE for that candidate only.
	ProbeTimeout time.Duration
}

func NewResolver(pool *pgxpool.Pool) *Resolver {
	return &Resolver{pool: pool, MaxCandidates: MaxSharedCandidates, ProbeTimeout: 5 * time.Second}
}

type Result struct {
	Outcome
	Stay string
	// Replayed: the request id was already resolved successfully and returned unchanged; never true for a non-success.
	Replayed bool
	// Candidates is the full vector the decision used, in stable interface order.
	Candidates []Candidate
}

// ReasonSpentOnRefusal: the request id carries a recorded refusal but re-evaluation now succeeds. The record
// can't be corrected (scd holds INSERT, deliberately not UPDATE, on iam_v2.auth_resolutions, so the audit
// trail can't be rewritten), and admitting a guest whose authority can't be recorded is refused. A conforming
// client never reaches this (each submission has its own id); it means a re-used id, logged as a distinct condition.
const ReasonSpentOnRefusal = "REQUEST_ID_SPENT_ON_AN_EARLIER_REFUSAL"

// Resolve waits for the whole vector (a slow VERIFIED must still beat a fast NO_MATCH) and records the
// decision idempotently by request id; concurrent retries converge on one stored resolution.
func (r *Resolver) Resolve(ctx context.Context, tenant, site, guestNetwork, requestID string, p Probe) (Result, error) {
	var res Result
	if requestID == "" {
		return res, ErrNoRequestID
	}
	// Only a stored VERIFIED resolution is replayed (same request id, same Stay, so a double tap converges on
	// one Auth Context). A stored refusal is not binding: the guest may have corrected a typo, so it is
	// re-evaluated below with every probe run. Replaying it froze the portal on the first mistake.
	prev, spent, err := r.load(ctx, tenant, site, requestID)
	if err != nil {
		return res, err
	}
	if spent && prev.Resolution == ResVerified {
		prev.Replayed = true
		return prev, nil
	}

	// From the trusted guest network mapping, never a client hint; ordered for a stable vector across retries.
	ifaces, err := r.mapped(ctx, tenant, site, guestNetwork)
	if err != nil {
		return res, err
	}

	cands := make([]Candidate, len(ifaces))
	stays := make([]string, len(ifaces))
	var wg sync.WaitGroup
	for i, m := range ifaces {
		if !m.Active {
			// Part of the explicit set, so it can neither be skipped (its guests would be invisible to the vector) nor asked.
			cands[i] = Candidate{InterfaceID: m.ID, Outcome: Unavailable, NotActive: true}
			continue
		}
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
				// A failed, timed-out or empty probe is indeterminate, never NO_MATCH.
				out, stay = Unavailable, ""
			}
			cands[i] = Candidate{InterfaceID: id, Outcome: out}
			if out == Verified {
				stays[i] = stay
			}
		}(i, m.ID)
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
		// VERIFIED without a Stay proves nothing: fail closed.
		if stay == "" {
			o = Outcome{Resolution: ResIndeterminate, Reason: "VERIFIED_WITHOUT_STAY"}
		}
	}
	res.Outcome, res.Stay, res.Candidates = o, stay, cands
	if o.Resolution != ResVerified {
		res.Stay = ""
	}

	// The unique index makes recording idempotent; when it refuses the insert (an earlier refusal being
	// re-evaluated, or a concurrent racer) the STORED row is reconciled with this result.
	if spent {
		return r.reconcile(prev, res, cands), nil
	}
	stored, err := r.store(ctx, tenant, site, guestNetwork, requestID, res)
	if err != nil {
		return Result{}, err
	}
	if stored != nil {
		return r.reconcile(*stored, res, cands), nil
	}
	return res, nil
}

// reconcile follows from the append-only record (see ReasonSpentOnRefusal):
//   - stored VERIFIED: the success stands, so one request id always sees one Stay;
//   - stored refused, fresh refused: the fresh result is the answer, nothing new is written;
//   - stored refused, fresh VERIFIED: irreconcilable, fails closed since the authority is unrecordable.
func (r *Resolver) reconcile(stored, fresh Result, cands []Candidate) Result {
	if stored.Resolution == ResVerified {
		stored.Candidates = cands
		stored.Replayed = true
		return stored
	}
	if fresh.Resolution == ResVerified {
		return Result{
			Outcome:    Outcome{Resolution: ResIndeterminate, Reason: ReasonSpentOnRefusal},
			Candidates: cands,
		}
	}
	return fresh
}

// mappedInterface is one interface the network's routing explicitly names, in service or not.
type mappedInterface struct {
	ID     string
	Active bool
}

// mapped returns every interface the network's routing names, whatever its lifecycle: an out-of-service candidate stays in the vector as unavailable instead of
// vanishing from it, because a strict resolution that quietly asked one fewer PMS could admit a guest the missing one would have contradicted.
func (r *Resolver) mapped(ctx context.Context, tenant, site, guestNetwork string) ([]mappedInterface, error) {
	rows, err := r.pool.Query(ctx, `SELECT m.pms_interface_id::text, COALESCE(pi.lifecycle_state,'') = 'ACTIVE'
		FROM iam_v2.guest_network_pms_map m
		LEFT JOIN iam_v2.pms_interfaces pi ON pi.tenant_id=m.tenant_id AND pi.site_id=m.site_id AND pi.id=m.pms_interface_id
		WHERE m.tenant_id=$1 AND m.site_id=$2 AND m.guest_network_id=$3
		ORDER BY m.pms_interface_id`, tenant, site, guestNetwork)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mappedInterface
	for rows.Next() {
		var m mappedInterface
		if err := rows.Scan(&m.ID, &m.Active); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
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

// store returns a non-nil Result when a concurrent racer resolved the id first: the stored outcome every caller must see.
func (r *Resolver) store(ctx context.Context, tenant, site, guestNetwork, requestID string, res Result) (*Result, error) {
	var stayArg any
	if res.Stay != "" {
		stayArg = res.Stay
	}
	// The capability scope lives for one transaction, so open it and insert in the same explicit transaction.
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := writerguard.Open(ctx, tx, writerguard.CapAuthResolution); err != nil {
		return nil, err
	}
	ct, err := tx.Exec(ctx, `INSERT INTO iam_v2.auth_resolutions
		(tenant_id, site_id, guest_network_id, resolved_stay_id, outcome_code, resolution_request_id)
		VALUES ($1,$2,$3,$4::uuid,$5,$6)
		ON CONFLICT (tenant_id, site_id, resolution_request_id) WHERE resolution_request_id IS NOT NULL
		DO NOTHING`,
		tenant, site, guestNetwork, stayArg, string(res.Resolution), requestID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
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
