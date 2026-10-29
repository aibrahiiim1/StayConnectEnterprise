package stayengine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/checkout"
	"github.com/stayconnect/enterprise/data-plane/internal/namenorm"

	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

// parseYYMMDD parses a Protel GA/GD date (YYMMDD); nil when absent or malformed, never fabricated.
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

// Processor applies durable inbox events (iam_v2.stay_events) to Stays, each in one transaction. It issues
// no financial command and writes no Posting.
type Processor struct {
	pool *pgxpool.Pool
	// conv, when set, runs the whole checkout conversion inside the event's transaction.
	conv CheckoutConverter
}

// ErrCheckoutConverterRequired: a GO event with no Converter wired fails closed (no unverified server-clock
// boundary); the transaction rolls back and the event stays PENDING.
var ErrCheckoutConverterRequired = errors.New("stayengine: checkout requires a wired Checkout Converter")

// CheckoutConverter must not open its own transaction.
type CheckoutConverter interface {
	ConvertTx(ctx context.Context, tx pgx.Tx, tenant, site, iface, stayID string, src checkout.BoundarySource) (checkout.Result, error)
}

func NewProcessor(pool *pgxpool.Pool) *Processor { return &Processor{pool: pool} }

// NewProcessorWithCheckout makes a GO event's application and its conversion one transaction.
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
	// Sharers is the optional full occupancy list; without it the name fields describe the primary guest.
	Sharers []Sharer `json:"sharers"`
	// FIAS A0/A1 attributes; absent means not stated, so the stay's value is left alone.
	TravelAgent *string `json:"travel_agent"`
	VIP         *bool   `json:"vip"`
}

// ProcessNext applies one pending consumable event for the scope; processed=false when none is pending.
// It claims with FOR UPDATE SKIP LOCKED and locks the reservation's Stay.
func (p *Processor) ProcessNext(ctx context.Context, tenant, site, iface string) (bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := writerguard.Open(ctx, tx, writerguard.CapStay); err != nil {
		return false, err
	}

	// Events for one interface are an ordered stream (GI before GO). SKIP LOCKED alone would let two
	// processors apply GO before GI exists; this advisory lock serializes application per
	// (tenant, site, interface) so the ORDER BY received_at, id claim is a true ordering guarantee.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || $2 || ':' || $3 || ':stay-events', 0))`,
		tenant, site, iface); err != nil {
		return false, err
	}

	var eventID, identity, eventType string
	var raw []byte
	// Occupancy evidence is stamped from the received time, clock flag and pinned revision (stampOccupancyEvidence).
	var evReceivedAt time.Time
	var evClockSuspect bool
	// pms_timestamp_utc is nullable; a non-pointer would fail every event without a PMS time.
	var evPMSAt *time.Time
	var pinnedRevision string
	var pinnedNormVersion int
	err = tx.QueryRow(ctx, `SELECT se.id::text, se.external_event_identity, se.event_type, se.payload,
		       se.received_at, se.clock_suspect, se.pms_timestamp_utc, COALESCE(r.pinned_revision_id::text,''),
		       COALESCE((SELECT rv.normalization_version FROM iam_v2.pms_interface_revisions rv
		                  WHERE rv.id = r.pinned_revision_id), 0)
		FROM iam_v2.stay_events se
		JOIN iam_v2.pms_interface_runtime r USING (tenant_id, site_id, pms_interface_id)
		WHERE se.tenant_id=$1 AND se.site_id=$2 AND se.pms_interface_id=$3
		  AND se.processing_status='PENDING'
		  AND (se.admission_kind='LIVE' OR se.resync_generation <= r.published_resync_generation)
		ORDER BY se.received_at, se.id
		FOR UPDATE OF se SKIP LOCKED
		LIMIT 1`, tenant, site, iface).Scan(&eventID, &identity, &eventType, &raw,
		&evReceivedAt, &evClockSuspect, &evPMSAt, &pinnedRevision, &pinnedNormVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}

	var pl payload
	if uerr := json.Unmarshal(raw, &pl); uerr != nil {
		if ferr := failEvent(ctx, tx, eventID, "MANUAL_REVIEW", "PAYLOAD_UNPARSEABLE"); ferr != nil {
			return false, ferr
		}
		return true, tx.Commit(ctx)
	}

	ev := InboxEvent{
		EventIdentity: identity, EventType: EventType(eventType),
		Reservation: pl.Reservation, Room: pl.Room, LastName: pl.LastName, FirstName: pl.FirstName,
		Folio: pl.Folio, ArrivalRaw: pl.ArrivalRaw, DepartureRaw: pl.Departure, Sharers: pl.Sharers,
		TravelAgent: pl.TravelAgent, VIP: pl.VIP,
	}

	// A self-contradicting payload goes to review before anything is written.
	if code := validateSharers(ev.Sharers); code != "" {
		if ferr := failEvent(ctx, tx, eventID, "MANUAL_REVIEW", code); ferr != nil {
			return false, ferr
		}
		return true, tx.Commit(ctx)
	}

	// One authoritative Stay row per reservation per interface; locked against concurrent processors.
	var cur *StayView
	var sv StayView
	var lerr error
	if strings.TrimSpace(ev.Reservation) == "" {
		// This PMS sends every departure as a GO with a room and no reservation. Exactly one IN_HOUSE match
		// in the room is the answer; several are sharers, and guessing would check out a resident guest, so
		// they go to MANUAL_REVIEW. No match leaves cur nil (GO_UNKNOWN_STAY). Only the lookup differs from a
		// reservation-keyed GO.
		if ev.EventType != EvGuestOut {
			// Only a checkout may be room-keyed.
			if ferr := failEvent(ctx, tx, eventID, "MANUAL_REVIEW", "EVENT_WITHOUT_RESERVATION"); ferr != nil {
				return false, ferr
			}
			return true, tx.Commit(ctx)
		}
		var n int
		if cerr := tx.QueryRow(ctx, `SELECT count(*) FROM iam_v2.stays
			WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3
			  AND status='IN_HOUSE' AND normalized_room_number=$4`,
			tenant, site, iface, normRoom(ev.Room)).Scan(&n); cerr != nil {
			return false, cerr
		}
		if n > 1 {
			if ferr := failEvent(ctx, tx, eventID, "MANUAL_REVIEW", "GO_AMBIGUOUS_ROOM"); ferr != nil {
				return false, ferr
			}
			return true, tx.Commit(ctx)
		}
		// A room number is not an identity: an unresolved GO stays PENDING and is re-offered after every
		// reconnect, possibly against a later occupant, whom the Converter would check out. The candidate
		// must already have arrived when the event was raised (stays.arrival, not occupancy_evidence_at;
		// see roomDepartureIsStale).
		var arrival *time.Time
		lerr = tx.QueryRow(ctx, `SELECT id::text, status, lifecycle_version, COALESCE(normalized_room_number,''),
			       arrival::timestamptz
			FROM iam_v2.stays
			WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3
			  AND status='IN_HOUSE' AND normalized_room_number=$4
			FOR UPDATE`, tenant, site, iface, normRoom(ev.Room)).
			Scan(&sv.ID, &sv.Status, &sv.LifecycleVersion, &sv.Room, &arrival)
		if lerr == nil && roomDepartureIsStale(eventAtFor(evPMSAt, evReceivedAt, evClockSuspect), arrival) {
			if ferr := failEvent(ctx, tx, eventID, "MANUAL_REVIEW", "GO_STALE_ROOM_DEPARTURE"); ferr != nil {
				return false, ferr
			}
			return true, tx.Commit(ctx)
		}
	} else {
		lerr = tx.QueryRow(ctx, `SELECT id::text, status, lifecycle_version, COALESCE(normalized_room_number,'')
			FROM iam_v2.stays
			WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 AND external_reservation_id=$4
			FOR UPDATE`, tenant, site, iface, ev.Reservation).
			Scan(&sv.ID, &sv.Status, &sv.LifecycleVersion, &sv.Room)
	}
	if lerr == nil {
		cur = &sv
	} else if !errors.Is(lerr, pgx.ErrNoRows) {
		return false, lerr
	}

	d := Resolve(ev, cur)
	occ := occupancyEvidence{At: evReceivedAt, RevisionID: pinnedRevision,
		NormalizationVersion: pinnedNormVersion, ClockSuspect: evClockSuspect}
	stayID, terminal, reviewCode, aerr := applyDecision(ctx, tx, tenant, site, iface, ev, cur, d, p.conv != nil, occ)
	if aerr != nil {
		return false, aerr
	}
	if err := finishEvent(ctx, tx, eventID, terminal, stayID, reviewCode); err != nil {
		return false, err
	}
	// The event is APPLIED with its lineage pinned so the Converter's audit can cite it; any failure rolls
	// everything back together.
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

// occupancyEvidence is taken from the event that confirmed the Stay is occupied.
type occupancyEvidence struct {
	At                   time.Time
	RevisionID           string
	NormalizationVersion int
	ClockSuspect         bool
}

// applyDecision performs the Stay mutation inside the caller's transaction and returns the stay id (or ""),
// the terminal processing_status and the review code.
func applyDecision(ctx context.Context, tx pgx.Tx, tenant, site, iface string, ev InboxEvent, cur *StayView, d Decision, delegateCheckout bool, occ occupancyEvidence) (stayID, terminal, reviewCode string, err error) {
	switch d.Op {
	case OpCreateStay:
		if stayID, err = createStay(ctx, tx, tenant, site, iface, ev); err != nil {
			return "", "", "", err
		}
		if err = applyGuestAttributes(ctx, tx, stayID, ev); err != nil {
			return "", "", "", err
		}
		if _, aerr := applyOccupancyFacts(ctx, tx, tenant, site, iface, stayID, ev); aerr != nil {
			return "", "", "", aerr
		}
		if serr := stampOccupancyEvidence(ctx, tx, stayID, occ); serr != nil {
			return "", "", "", serr
		}
		return stayID, "APPLIED", "", nil

	case OpUpdateStay:
		// AD-37 step 2: a record that changes nothing only re-stamps the occupancy evidence (freshness) and is applied with
		// its lineage pinned by finishEvent; it does not rewrite the stay or its occupants.
		if d.NewStatus == "" {
			same, uerr := recordUnchanged(ctx, tx, tenant, site, cur.ID, ev)
			if uerr != nil {
				return "", "", "", uerr
			}
			if same {
				if serr := stampOccupancyEvidence(ctx, tx, cur.ID, occ); serr != nil {
					return "", "", "", serr
				}
				fastPathApplied.Add(1)
				return cur.ID, "APPLIED", "", nil
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE iam_v2.stays SET
			status = CASE WHEN $2<>'' THEN $2 ELSE status END,
			arrival = COALESCE($3, arrival), departure = COALESCE($4, departure)
			WHERE id=$1`, cur.ID, d.NewStatus, parseYYMMDD(ev.ArrivalRaw), parseYYMMDD(ev.DepartureRaw)); err != nil {
			return "", "", "", err
		}
		if err = applyGuestAttributes(ctx, tx, cur.ID, ev); err != nil {
			return "", "", "", err
		}
		if _, aerr := applyOccupancyFacts(ctx, tx, tenant, site, iface, cur.ID, ev); aerr != nil {
			return "", "", "", aerr
		}
		if serr := stampOccupancyEvidence(ctx, tx, cur.ID, occ); serr != nil {
			return "", "", "", serr
		}
		return cur.ID, "APPLIED", "", nil

	case OpRoomMove:
		if _, err = tx.Exec(ctx, `UPDATE iam_v2.stays SET normalized_room_number=NULLIF($2,'') WHERE id=$1`, cur.ID, namenorm.Room(ev.Room)); err != nil {
			return "", "", "", err
		}
		if err = applyGuestAttributes(ctx, tx, cur.ID, ev); err != nil {
			return "", "", "", err
		}
		if serr := stampOccupancyEvidence(ctx, tx, cur.ID, occ); serr != nil {
			return "", "", "", serr
		}
		return cur.ID, "APPLIED", "", nil

	case OpCheckout:
		// The Converter owns the checkout boundary; without one this fails closed.
		if !delegateCheckout {
			return "", "", "", ErrCheckoutConverterRequired
		}
		return cur.ID, "APPLIED", "", nil

	case OpReinstate:
		// The lifecycle trigger requires exactly one version bump and a cleared effective_checkout_at.
		if _, err = tx.Exec(ctx, `UPDATE iam_v2.stays SET status='IN_HOUSE', lifecycle_version=lifecycle_version+1,
			effective_checkout_at=NULL WHERE id=$1`, cur.ID); err != nil {
			return "", "", "", err
		}
		if err = applyGuestAttributes(ctx, tx, cur.ID, ev); err != nil {
			return "", "", "", err
		}
		if serr := stampOccupancyEvidence(ctx, tx, cur.ID, occ); serr != nil {
			return "", "", "", serr
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

// applyGuestAttributes writes only the VIP and travel-agent values the record stated; a stated empty travel
// agent clears it.
func applyGuestAttributes(ctx context.Context, tx pgx.Tx, stayID string, ev InboxEvent) error {
	if ev.TravelAgent == nil && ev.VIP == nil {
		return nil
	}
	var agent any
	if ev.TravelAgent != nil {
		agent = *ev.TravelAgent
	}
	var vip any
	if ev.VIP != nil {
		vip = *ev.VIP
	}
	_, err := tx.Exec(ctx, `UPDATE iam_v2.stays SET
		travel_agent = CASE WHEN $2::text IS NULL THEN travel_agent ELSE NULLIF($2::text,'') END,
		vip = COALESCE($3::bool, vip)
		WHERE id=$1`, stayID, agent, vip)
	return err
}

// createStay inserts the IN_HOUSE Stay; external_stay_identity equals the reservation.
func createStay(ctx context.Context, tx pgx.Tx, tenant, site, iface string, ev InboxEvent) (string, error) {
	var stayID string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.stays
		(tenant_id, site_id, pms_interface_id, external_reservation_id, external_stay_identity,
		 normalized_room_number, status, lifecycle_version, arrival, departure)
		VALUES ($1,$2,$3,$4,$4, NULLIF($5,''), 'IN_HOUSE', 1, $6, $7)
		RETURNING id::text`,
		tenant, site, iface, ev.Reservation, namenorm.Room(ev.Room),
		parseYYMMDD(ev.ArrivalRaw), parseYYMMDD(ev.DepartureRaw)).Scan(&stayID); err != nil {
		return "", err
	}
	return stayID, nil
}

// applyOccupancyFacts writes the event's occupancy: the name fields as the primary guest when there is no
// sharer list, otherwise the reconciled list.
func applyOccupancyFacts(ctx context.Context, tx pgx.Tx, tenant, site, iface, stayID string, ev InboxEvent) (string, error) {
	if len(ev.Sharers) == 0 {
		if ev.FirstName != "" || ev.LastName != "" {
			if err := upsertPrimaryGuest(ctx, tx, tenant, site, iface, stayID, ev); err != nil {
				return "", err
			}
		}
	} else if err := applySharers(ctx, tx, tenant, site, iface, stayID, ev.Sharers); err != nil {
		return "", err
	}
	return "", nil
}

func upsertPrimaryGuest(ctx context.Context, tx pgx.Tx, tenant, site, iface, stayID string, ev InboxEvent) error {
	display := ev.LastName
	if ev.FirstName != "" && ev.LastName != "" {
		display = ev.LastName + ", " + ev.FirstName
	} else if ev.LastName == "" {
		display = ev.FirstName
	}
	// The `_norm` columns must hold normalized values (authentication compares upper-cased input); `display`
	// stays raw so operators see the PMS spelling.
	_, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_guests
		(tenant_id, site_id, pms_interface_id, stay_id, first_name_norm, last_name_norm, display_name, is_primary)
		VALUES ($1,$2,$3,$4, NULLIF($5,''), NULLIF($6,''), NULLIF($7,''), true)
		ON CONFLICT (stay_id) WHERE is_primary
		DO UPDATE SET first_name_norm=EXCLUDED.first_name_norm, last_name_norm=EXCLUDED.last_name_norm,
		             display_name=EXCLUDED.display_name`,
		tenant, site, iface, stayID,
		namenorm.Name(ev.FirstName), namenorm.Name(ev.LastName), display)
	return err
}

// finishEvent moves the event PENDING to terminal exactly once and, when APPLIED, records the stay lineage.
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
		// The checkout boundary verifier relies on this exact event lineage.
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

// stampOccupancyEvidence records that the PMS just confirmed the Stay is occupied. Authentication
// (internal/authctx, iam_v2.issue_or_return_pms_context) refuses Stays whose evidence is absent,
// clock-suspect, from another Revision, or stale.
//
// Values come from the event, not the server clock. Stamp for arrivals, updates, room moves and
// reinstatements, never for a checkout, which must not refresh occupancy.
//
// Database rules shaping the statement:
//   - stays_occupancy_all_or_none: evidence_at, ingested_at, revision_id, normalization_version and
//     clock_suspect are one tuple; a partial write aborts the event.
//   - stays_evidence_version_coherent: evidence present <=> version > 0.
//   - p3_stay_lifecycle_guard: a material change (all of the tuple except ingested_at) must bump the version
//     by exactly 1 and a non-material write must not touch it, hence the CASE.
func stampOccupancyEvidence(ctx context.Context, tx pgx.Tx, stayID string, occ occupancyEvidence) error {
	if stayID == "" || occ.At.IsZero() || occ.RevisionID == "" || occ.NormalizationVersion <= 0 {
		// No pinned revision is no reason to reject the Stay; authentication just keeps declining it.
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE iam_v2.stays
		   SET occupancy_evidence_at            = $2,
		       occupancy_ingested_at            = now(),
		       occupancy_revision_id            = $3::uuid,
		       occupancy_normalization_version  = $4,
		       occupancy_clock_suspect          = $5,
		       occupancy_evidence_version       = occupancy_evidence_version + CASE
		         WHEN occupancy_evidence_at           IS DISTINCT FROM $2
		           OR occupancy_revision_id           IS DISTINCT FROM $3::uuid
		           OR occupancy_normalization_version IS DISTINCT FROM $4
		           OR occupancy_clock_suspect         IS DISTINCT FROM $5
		         THEN 1 ELSE 0 END
		 WHERE id = $1`, stayID, occ.At, occ.RevisionID, occ.NormalizationVersion, occ.ClockSuspect)
	return err
}
