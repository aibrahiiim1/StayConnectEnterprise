package stayengine

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Sharer is one occupant of a Stay as the PMS reports it. Sharers are LEGAL and ordinary: a Stay may have any
// number of them, and exactly one is the primary. The external id is INTERFACE-SCOPED, never global.
type Sharer struct {
	ExternalGuestID string `json:"external_guest_id"`
	FirstName       string `json:"first_name"`
	LastName        string `json:"last_name"`
	IsPrimary       bool   `json:"is_primary"`
}

// ErrSourceConflict — the event's occupancy/folio facts cannot be applied without overwriting another Stay's
// facts or guessing between contradictory ones. It is routed to MANUAL_REVIEW rather than resolved silently.
var ErrSourceConflict = errors.New("stayengine: source conflict")

// conflict codes (bounded machine codes recorded on the event)
const (
	CodeSharerDuplicate   = "SHARER_DUPLICATE_IDENTITY"
	CodeSharerTwoPrimary  = "SHARER_MULTIPLE_PRIMARY"
	CodeFolioStayConflict = "FOLIO_CLAIMED_BY_OTHER_STAY"
)

// validateSharers rejects a payload that contradicts itself BEFORE anything is written: the same guest listed
// twice, or two occupants both claiming to be the primary. Guessing between them would silently pick a winner.
func validateSharers(sharers []Sharer) string {
	seen := map[string]bool{}
	primaries := 0
	for _, s := range sharers {
		id := strings.TrimSpace(s.ExternalGuestID)
		if id != "" {
			if seen[id] {
				return CodeSharerDuplicate
			}
			seen[id] = true
		}
		if s.IsPrimary {
			primaries++
		}
	}
	if primaries > 1 {
		return CodeSharerTwoPrimary
	}
	return ""
}

// applySharers reconciles the Stay's occupants with the event. Occupants are keyed by their interface-scoped
// external guest id; an occupant with no external id is matched on its normalized name instead (some PMS
// profiles carry no id). Exactly one primary survives: the primary flag is moved atomically, never duplicated,
// because one_primary_guest_per_stay is a real unique index and a second primary would abort the whole event.
func applySharers(ctx context.Context, tx pgx.Tx, tenant, site, iface, stayID string, sharers []Sharer) error {
	for _, s := range sharers {
		display := displayName(s.FirstName, s.LastName)
		if s.IsPrimary {
			// demote the current primary first; the index allows exactly one at a time.
			if _, err := tx.Exec(ctx, `UPDATE iam_v2.stay_guests SET is_primary=false
				WHERE stay_id=$1 AND is_primary AND COALESCE(external_guest_id,'') <> $2`, stayID, s.ExternalGuestID); err != nil {
				return err
			}
		}
		var id string
		err := tx.QueryRow(ctx, `SELECT id::text FROM iam_v2.stay_guests
			WHERE stay_id=$1 AND (
			      (COALESCE($2,'') <> '' AND external_guest_id = $2)
			   OR (COALESCE($2,'') = '' AND COALESCE(first_name_norm,'')=COALESCE(NULLIF($3,''),'')
			       AND COALESCE(last_name_norm,'')=COALESCE(NULLIF($4,''),'')))
			LIMIT 1`, stayID, s.ExternalGuestID, s.FirstName, s.LastName).Scan(&id)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_guests
				(tenant_id, site_id, pms_interface_id, stay_id, external_guest_id, first_name_norm, last_name_norm, display_name, is_primary)
				VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),$9)`,
				tenant, site, iface, stayID, s.ExternalGuestID, s.FirstName, s.LastName, display, s.IsPrimary); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if _, err := tx.Exec(ctx, `UPDATE iam_v2.stay_guests SET
				first_name_norm=COALESCE(NULLIF($1,''),first_name_norm),
				last_name_norm=COALESCE(NULLIF($2,''),last_name_norm),
				display_name=COALESCE(NULLIF($3,''),display_name),
				is_primary = CASE WHEN $4 THEN true ELSE is_primary END
				WHERE id=$5`, s.FirstName, s.LastName, display, s.IsPrimary, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyFolio links the event's folio to the Stay as its default posting target. Folio identity is
// INTERFACE-SCOPED and only ONE folio may be open per external id (folio_open_identity). A GUEST folio that is
// already the posting target of a DIFFERENT Stay on the same interface is a SOURCE CONFLICT: silently
// re-pointing it would move one Stay's postings onto another, so the event goes to MANUAL_REVIEW instead.
// COMPANY / GROUP_MASTER folios are legitimately shared and are linked without stealing the default target.
func applyFolio(ctx context.Context, tx pgx.Tx, tenant, site, iface, stayID, externalFolioID string) (string, error) {
	externalFolioID = strings.TrimSpace(externalFolioID)
	if externalFolioID == "" {
		return "", nil
	}
	var folioID, kind string
	err := tx.QueryRow(ctx, `SELECT id::text, folio_kind FROM iam_v2.folios
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 AND external_folio_id=$4 AND status='OPEN'
		FOR UPDATE`, tenant, site, iface, externalFolioID).Scan(&folioID, &kind)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.folios
			(tenant_id, site_id, pms_interface_id, external_folio_id, folio_kind, status)
			VALUES ($1,$2,$3,$4,'GUEST','OPEN') RETURNING id::text`,
			tenant, site, iface, externalFolioID).Scan(&folioID); err != nil {
			return "", err
		}
		kind = "GUEST"
	case err != nil:
		return "", err
	}

	if kind == "GUEST" {
		var otherStay string
		err := tx.QueryRow(ctx, `SELECT stay_id::text FROM iam_v2.stay_folios
			WHERE folio_id=$1 AND stay_id <> $2 AND is_default_posting_target LIMIT 1`, folioID, stayID).Scan(&otherStay)
		if err == nil && otherStay != "" {
			return CodeFolioStayConflict, ErrSourceConflict
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}

	isDefault := kind == "GUEST"
	if isDefault {
		// exactly one default posting target per Stay (stay_folio_default): clear the previous one first.
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.stay_folios SET is_default_posting_target=false
			WHERE stay_id=$1 AND is_default_posting_target AND folio_id <> $2`, stayID, folioID); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_folios
		(tenant_id, site_id, pms_interface_id, stay_id, folio_id, is_default_posting_target)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (stay_id, folio_id) DO UPDATE SET is_default_posting_target=EXCLUDED.is_default_posting_target`,
		tenant, site, iface, stayID, folioID, isDefault); err != nil {
		return "", err
	}
	return "", nil
}

func displayName(first, last string) string {
	switch {
	case first != "" && last != "":
		return last + ", " + first
	case last != "":
		return last
	default:
		return first
	}
}

// precheckFolioConflict reports the bounded conflict code when the event's folio is an OPEN GUEST folio that
// is already the default posting target of a DIFFERENT Stay on this interface. It runs BEFORE any write, so a
// conflicting event is routed to review without leaving partially applied facts behind. Company/group-master
// folios are legitimately shared and never conflict.
func precheckFolioConflict(ctx context.Context, tx pgx.Tx, tenant, site, iface, currentStayID, externalFolioID string) (string, error) {
	externalFolioID = strings.TrimSpace(externalFolioID)
	if externalFolioID == "" {
		return "", nil
	}
	var other string
	err := tx.QueryRow(ctx, `SELECT sf.stay_id::text
		FROM iam_v2.folios f JOIN iam_v2.stay_folios sf ON sf.folio_id=f.id
		WHERE f.tenant_id=$1 AND f.site_id=$2 AND f.pms_interface_id=$3 AND f.external_folio_id=$4
		  AND f.status='OPEN' AND f.folio_kind='GUEST'
		  AND sf.is_default_posting_target AND sf.stay_id::text <> COALESCE(NULLIF($5,''),'00000000-0000-0000-0000-000000000000')
		LIMIT 1`, tenant, site, iface, externalFolioID, currentStayID).Scan(&other)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return CodeFolioStayConflict, nil
}
