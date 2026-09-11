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
	// Replayed is true when this request id had already been resolved SUCCESSFULLY and that stored outcome was
	// returned unchanged (the probes are not re-run and no second row is written). It is never true for a
	// non-success: see Resolve.
	Replayed bool
	// Candidates is the complete vector the decision was made on, in stable interface order.
	Candidates []Candidate
}

// ReasonSpentOnRefusal — this request id already carries a RECORDED REFUSAL, and re-evaluating it against the
// evidence presented now produced a success. The two cannot both be true of one resolution request, and the
// recorded one cannot be corrected: scd holds INSERT on iam_v2.auth_resolutions and deliberately not UPDATE,
// because a service that can rewrite the record of why a guest was admitted is a service whose audit trail
// proves nothing. Admitting a guest whose authority cannot be recorded is the failure this refuses.
//
// No conforming caller reaches it. A request id identifies ONE deliberate submission, so a second submission
// carries a second id and is resolved and recorded on its own terms. Reaching this means a client re-used a
// spent id — a stale portal build, or something that is not the portal — so the guest gets the uniform
// refusal and scd logs it as the distinct condition it is, rather than as one more wrong surname.
const ReasonSpentOnRefusal = "REQUEST_ID_SPENT_ON_AN_EARLIER_REFUSAL"

// Resolve gathers every mapped interface's verdict CONCURRENTLY, waits for the whole vector (a slow VERIFIED
// must still beat a fast NO_MATCH), applies the strict decision, and records it IDEMPOTENTLY against the
// caller's request id. Concurrent retries of the same request id converge on one stored resolution.
func (r *Resolver) Resolve(ctx context.Context, tenant, site, guestNetwork, requestID string, p Probe) (Result, error) {
	var res Result
	if requestID == "" {
		return res, ErrNoRequestID
	}
	// (1) ALREADY RESOLVED? REPLAY IS A PROPERTY OF SUCCESS, AND ONLY OF SUCCESS.
	//
	// A stored VERIFIED resolution is replayed verbatim, without re-probing anything. That is what makes a
	// successful identity proof idempotent: the same request id yields the same Stay, so a double tap or a
	// reply the guest's phone never received converges on the one Auth Context instead of minting a second.
	//
	// A stored NON-VERIFIED resolution is NOT a verdict binding on anything that comes later. It is the record
	// of one refused attempt, and the evidence in front of us now may be different — a guest who mistyped a
	// surname and corrected it presents a different fact about the world than the one that was refused. This
	// used to replay it, and that froze the portal page: the first typo answered every later submission, and
	// because every non-success renders one uniform sentence, the guest could not tell that their correction
	// had never been compared against anything at all.
	//
	// So a request id whose stored outcome is anything other than VERIFIED is RE-EVALUATED below, against
	// current state, with every probe actually run. Nothing is replayed.
	prev, spent, err := r.load(ctx, tenant, site, requestID)
	if err != nil {
		return res, err
	}
	if spent && prev.Resolution == ResVerified {
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

	// (4) RECORD. The unique index is what makes this idempotent, and the interesting case is the one where it
	// refuses the insert — either because the id already carried a refusal that step (1) chose to re-evaluate,
	// or because a concurrent racer on the same id got there first. Both hand back the STORED row, and both
	// are answered by the same rule.
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

// reconcile answers what a caller sees when a request id already carries a stored resolution and the probes
// have just been run again for it. `stored` is the durable record; `fresh` is what this evaluation decided.
//
// The rule follows from the record being append-only (see ReasonSpentOnRefusal):
//
//   - stored VERIFIED — the success stands and is replayed. Every caller of one request id sees one Stay,
//     which is what stops a retry becoming a second Auth Context.
//   - stored refused, fresh refused — the fresh evaluation IS the answer. The probes genuinely ran; the guest
//     is refused because they are refused now, not because they were refused before. The stored row already
//     records the attempt, so nothing new is written.
//   - stored refused, fresh VERIFIED — irreconcilable. One request id cannot be both, and the refusal cannot
//     be overwritten, so this fails closed rather than granting access whose authority is unrecordable.
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
	// The resolution record is capability-scoped, and a scope lives for exactly one transaction. That is why
	// this runs in an explicit transaction rather than as a single pool statement: opening the scope on the
	// pool and inserting on the pool are two transactions, and the second would find no scope open.
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
