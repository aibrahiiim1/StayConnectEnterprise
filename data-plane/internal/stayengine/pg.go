package stayengine

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/checkout"
)

// parseYYMMDD parses a Protel GA/GD date (YYMMDD) into a date, or nil when absent/malformed (dates are
// evidence, never identity — an unparseable one is simply not stored, never fabricated).
func parseYYMMDD(s string) *time.Time {
	if len(s) != 6 {
		return nil
	}
	t, err := time.Parse("060102", s)
	if err != nil {
		return nil
	}
	return &t
}

// Processor is the transactional Stay ingestion engine. It consumes durable inbox rows (iam_v2.stay_events)
// that are consumable — LIVE, or RESYNC whose generation is published — and applies each in ONE PostgreSQL
// transaction, moving the event PENDING→terminal exactly once. It issues NO financial command and writes NO
// Posting; Folios are identity records only.
type Processor struct {
	pool *pgxpool.Pool
	// conv, when set, makes Checkout ONE PHYSICAL TRANSACTION with the Event application: the engine skips its
	// own Stay flip and delegates the whole boundary/eligibility/Grace/device/session/audit conversion to the
	// Checkout Converter inside this same tx. Nil keeps the legacy Stay-domain-only flip.
	conv CheckoutConverter
}

// ErrCheckoutConverterRequired — a Checkout (GO) event was claimed but no Checkout Converter is wired. There is
// no legacy Stay-domain-only checkout path: rather than establish an unverified server-clock boundary, the
// application fails closed and the whole transaction rolls back (the event stays PENDING for a correct retry).
var ErrCheckoutConverterRequired = errors.New("stayengine: checkout requires a wired Checkout Converter")

// CheckoutConverter is the transaction-bound Checkout conversion the engine delegates to (implemented by
// checkout.Converter). It must NOT open its own transaction.
type CheckoutConverter interface {
	ConvertTx(ctx context.Context, tx pgx.Tx, tenant, site, iface, stayID string, src checkout.BoundarySource) (checkout.Result, error)
}

func NewProcessor(pool *pgxpool.Pool) *Processor { return &Processor{pool: pool} }

// NewProcessorWithCheckout wires the Checkout Converter so a GO event's application and its conversion commit
// (or roll back) as ONE physical transaction.
func NewProcessorWithCheckout(pool *pgxpool.Pool, conv CheckoutConverter) *Processor {
	return &Processor{pool: pool, conv: conv}
}

// payload mirrors the connector's eventPayloadJSON (bounded typed fields only; never the raw frame).
type payload struct {
	Reservation string `json:"reservation"`
	Room        string `json:"room"`
	LastName    string `json:"last_name"`
	FirstName   string `json:"first_name"`
	Folio       string `json:"folio"`
	ArrivalRaw  string `json:"arrival_raw"`
	Departure   string `json:"departure_raw"`
}

// ProcessNext claims and processes ONE pending consumable inbox event for the scope in a single transaction.
// It returns processed=false when nothing is pending. Concurrency-safe: it FOR UPDATE SKIP LOCKED-claims the
// event and locks the reservation's Stay, so two processors never apply the same event or race one Stay.
func (p *Processor) ProcessNext(ctx context.Context, tenant, site, iface string) (bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// ORDERED APPLICATION. Events for one PMS Interface form an ordered stream: a Stay's GI must be applied
	// before its GO, and a room-move before a later correction. With only FOR UPDATE SKIP LOCKED, two processors
	// could claim GI and GO concurrently and apply the GO against a Stay that does not exist yet (an orphan
	// MANUAL_REVIEW) — i.e. silent reordering. This transaction-scoped advisory lock serializes application per
	// (tenant, site, interface), so the ORDER BY received_at, id claim below is a true ordering guarantee.
	// Different interfaces still process concurrently, and the lock is released at commit/rollback.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || $2 || ':' || $3 || ':stay-events', 0))`,
		tenant, site, iface); err != nil {
		return false, err
	}

	var eventID, identity, eventType string
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT se.id::text, se.external_event_identity, se.event_type, se.payload
		FROM iam_v2.stay_events se
		JOIN iam_v2.pms_interface_runtime r USING (tenant_id, site_id, pms_interface_id)
		WHERE se.tenant_id=$1 AND se.site_id=$2 AND se.pms_interface_id=$3
		  AND se.processing_status='PENDING'
		  AND (se.admission_kind='LIVE' OR se.resync_generation <= r.published_resync_generation)
		ORDER BY se.received_at, se.id
		FOR UPDATE OF se SKIP LOCKED
		LIMIT 1`, tenant, site, iface).Scan(&eventID, &identity, &eventType, &raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}

	var pl payload
	if uerr := json.Unmarshal(raw, &pl); uerr != nil {
		// an unparseable durable payload is not silently applied — mark it for review.
		if ferr := failEvent(ctx, tx, eventID, "MANUAL_REVIEW", "PAYLOAD_UNPARSEABLE"); ferr != nil {
			return false, ferr
		}
		return true, tx.Commit(ctx)
	}

	ev := InboxEvent{
		EventIdentity: identity, EventType: EventType(eventType),
		Reservation: pl.Reservation, Room: pl.Room, LastName: pl.LastName, FirstName: pl.FirstName,
		Folio: pl.Folio, ArrivalRaw: pl.ArrivalRaw, DepartureRaw: pl.Departure,
	}

	// load the CURRENT stay for this reservation within the interface (one authoritative row per reservation;
	// lifecycle_version is the episode counter). Locked so a concurrent processor cannot race it.
	var cur *StayView
	var sv StayView
	lerr := tx.QueryRow(ctx, `SELECT id::text, status, lifecycle_version, COALESCE(normalized_room_number,'')
		FROM iam_v2.stays
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 AND external_reservation_id=$4
		FOR UPDATE`, tenant, site, iface, ev.Reservation).Scan(&sv.ID, &sv.Status, &sv.LifecycleVersion, &sv.Room)
	if lerr == nil {
		cur = &sv
	} else if !errors.Is(lerr, pgx.ErrNoRows) {
		return false, lerr
	}

	d := Resolve(ev, cur)
	stayID, terminal, reviewCode, aerr := applyDecision(ctx, tx, tenant, site, iface, ev, cur, d, p.conv != nil)
	if aerr != nil {
		return false, aerr
	}
	if err := finishEvent(ctx, tx, eventID, terminal, stayID, reviewCode); err != nil {
		return false, err
	}
	// ONE PHYSICAL TRANSACTION: the event is now APPLIED with the Stay application lineage pinned, so the
	// Converter's audit can cite it as the verified boundary event. Any failure here rolls the Event, Stay,
	// Entitlements, Purchase, Grace, devices, sessions and audit back together (no nested transaction).
	if d.Op == OpCheckout && p.conv != nil && stayID != "" && terminal == "APPLIED" {
		if _, cerr := p.conv.ConvertTx(ctx, tx, tenant, site, iface, stayID, checkout.BoundarySource{StayEventID: eventID}); cerr != nil {
			return false, cerr
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// applyDecision performs the authoritative Stay mutation for the decision and returns the resolved stay id (or
// ""), the terminal processing_status and the review code. All within the caller's transaction.
func applyDecision(ctx context.Context, tx pgx.Tx, tenant, site, iface string, ev InboxEvent, cur *StayView, d Decision, delegateCheckout bool) (stayID, terminal, reviewCode string, err error) {
	switch d.Op {
	case OpCreateStay:
		if stayID, err = createStay(ctx, tx, tenant, site, iface, ev); err != nil {
			return "", "", "", err
		}
		return stayID, "APPLIED", "", nil

	case OpUpdateStay:
		if _, err = tx.Exec(ctx, `UPDATE iam_v2.stays SET
			status = CASE WHEN $2<>'' THEN $2 ELSE status END,
			arrival = COALESCE($3, arrival), departure = COALESCE($4, departure)
			WHERE id=$1`, cur.ID, d.NewStatus, parseYYMMDD(ev.ArrivalRaw), parseYYMMDD(ev.DepartureRaw)); err != nil {
			return "", "", "", err
		}
		if err = upsertPrimaryGuest(ctx, tx, tenant, site, iface, cur.ID, ev); err != nil {
			return "", "", "", err
		}
		return cur.ID, "APPLIED", "", nil

	case OpRoomMove:
		if _, err = tx.Exec(ctx, `UPDATE iam_v2.stays SET normalized_room_number=NULLIF($2,'') WHERE id=$1`, cur.ID, ev.Room); err != nil {
			return "", "", "", err
		}
		return cur.ID, "APPLIED", "", nil

	case OpCheckout:
		// The Checkout Converter OWNS the boundary: it derives the trusted/conservative effective_checkout_at
		// from this durable event inside the SAME transaction. There is NO legacy server-clock flip — a Checkout
		// with no Converter wired FAILS CLOSED rather than silently establishing an unverified boundary.
		if !delegateCheckout {
			return "", "", "", ErrCheckoutConverterRequired
		}
		return cur.ID, "APPLIED", "", nil

	case OpReinstate:
		// CHECKED_OUT → IN_HOUSE with EXACTLY one lifecycle_version bump (the migration trigger enforces this) and
		// CLEARING the previous episode's boundary (the guard requires reinstatement to reset effective_checkout_at).
		if _, err = tx.Exec(ctx, `UPDATE iam_v2.stays SET status='IN_HOUSE', lifecycle_version=lifecycle_version+1,
			effective_checkout_at=NULL WHERE id=$1`, cur.ID); err != nil {
			return "", "", "", err
		}
		return cur.ID, "APPLIED", "", nil

	case OpSkipDuplicate:
		id := ""
		if cur != nil {
			id = cur.ID
		}
		return id, "SKIPPED_DUPLICATE", "", nil

	default: // OpManualReview
		return "", "MANUAL_REVIEW", d.ReviewCode, nil
	}
}

// createStay inserts the Stay (IN_HOUSE, lifecycle_version 1), its primary Guest, and — if a Folio is present
// — a Folio IDENTITY record linked as the default posting target (identity only; no financial state).
func createStay(ctx context.Context, tx pgx.Tx, tenant, site, iface string, ev InboxEvent) (string, error) {
	var stayID string
	// external_stay_identity == reservation: one authoritative Stay per reservation per interface, episodes
	// tracked by lifecycle_version.
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.stays
		(tenant_id, site_id, pms_interface_id, external_reservation_id, external_stay_identity,
		 normalized_room_number, status, lifecycle_version, arrival, departure)
		VALUES ($1,$2,$3,$4,$4, NULLIF($5,''), 'IN_HOUSE', 1, $6, $7)
		RETURNING id::text`,
		tenant, site, iface, ev.Reservation, ev.Room, parseYYMMDD(ev.ArrivalRaw), parseYYMMDD(ev.DepartureRaw)).Scan(&stayID); err != nil {
		return "", err
	}
	if err := upsertPrimaryGuest(ctx, tx, tenant, site, iface, stayID, ev); err != nil {
		return "", err
	}
	if ev.Folio != "" {
		var folioID string
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.folios
			(tenant_id, site_id, pms_interface_id, external_folio_id, folio_kind, status)
			VALUES ($1,$2,$3,$4,'GUEST','OPEN')
			ON CONFLICT (tenant_id, site_id, pms_interface_id, external_folio_id) WHERE status='OPEN'
			DO UPDATE SET external_folio_id=EXCLUDED.external_folio_id
			RETURNING id::text`, tenant, site, iface, ev.Folio).Scan(&folioID); err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_folios
			(tenant_id, site_id, pms_interface_id, stay_id, folio_id, is_default_posting_target)
			VALUES ($1,$2,$3,$4,$5,true) ON CONFLICT (stay_id, folio_id) DO NOTHING`,
			tenant, site, iface, stayID, folioID); err != nil {
			return "", err
		}
	}
	return stayID, nil
}

// upsertPrimaryGuest inserts or refreshes the single primary guest for the Stay from the (validated) names.
func upsertPrimaryGuest(ctx context.Context, tx pgx.Tx, tenant, site, iface, stayID string, ev InboxEvent) error {
	display := ev.LastName
	if ev.FirstName != "" && ev.LastName != "" {
		display = ev.LastName + ", " + ev.FirstName
	} else if ev.LastName == "" {
		display = ev.FirstName
	}
	_, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_guests
		(tenant_id, site_id, pms_interface_id, stay_id, first_name_norm, last_name_norm, display_name, is_primary)
		VALUES ($1,$2,$3,$4, NULLIF($5,''), NULLIF($6,''), NULLIF($7,''), true)
		ON CONFLICT (stay_id) WHERE is_primary
		DO UPDATE SET first_name_norm=EXCLUDED.first_name_norm, last_name_norm=EXCLUDED.last_name_norm,
		             display_name=EXCLUDED.display_name`,
		tenant, site, iface, stayID, ev.FirstName, ev.LastName, display)
	return err
}

// finishEvent moves the event PENDING→terminal exactly once (guarded by processing_status='PENDING') and, for
// an APPLIED outcome, records the resolved stay lineage. The migration trigger enforces the result rules.
func finishEvent(ctx context.Context, tx pgx.Tx, eventID, terminal, stayID, reviewCode string) error {
	tag, err := tx.Exec(ctx, `UPDATE iam_v2.stay_events
		SET processing_status=$2, stay_id=NULLIF($3,'')::uuid, processed_at=now(), review_code=NULLIF($4,'')
		WHERE id=$1 AND processing_status='PENDING'`, eventID, terminal, stayID, reviewCode)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("stayengine: event no longer PENDING (raced)")
	}
	if terminal == "APPLIED" && stayID != "" {
		// pin the EXACT durable event whose application last advanced the Stay (item 3 lineage), alongside the
		// per-application counter, so the Checkout boundary verifier can prove exact event lineage.
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.stays
			SET last_applied_event_version = last_applied_event_version + 1, last_applied_event_id = $2::uuid
			WHERE id=$1`, stayID, eventID); err != nil {
			return err
		}
	}
	return nil
}

func failEvent(ctx context.Context, tx pgx.Tx, eventID, terminal, reviewCode string) error {
	return finishEvent(ctx, tx, eventID, terminal, "", reviewCode)
}
