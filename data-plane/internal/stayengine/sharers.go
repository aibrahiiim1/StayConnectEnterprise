package stayengine

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/namenorm"
)

// Sharer is one occupant of a Stay as the PMS reports it. Sharers are LEGAL and ordinary: a Stay may have any
// number of them, and exactly one is the primary. The external id is INTERFACE-SCOPED, never global.
type Sharer struct {
	ExternalGuestID string `json:"external_guest_id"`
	FirstName       string `json:"first_name"`
	LastName        string `json:"last_name"`
	IsPrimary       bool   `json:"is_primary"`
}

// ErrSourceConflict — the event's occupancy facts cannot be applied without overwriting another Stay's
// facts or guessing between contradictory ones. It is routed to MANUAL_REVIEW rather than resolved silently.
var ErrSourceConflict = errors.New("stayengine: source conflict")

// conflict codes (bounded machine codes recorded on the event)
const (
	CodeSharerDuplicate  = "SHARER_DUPLICATE_IDENTITY"
	CodeSharerTwoPrimary = "SHARER_MULTIPLE_PRIMARY"
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
		// THE SHARER PATH WRITES THE SAME `_norm` COLUMNS AND WAS MISSED BY THE FIRST PASS OF THIS FIX.
		// Primary-guest normalization alone would have left every additional occupant raw, so a second guest
		// on the booking still could not sign in.
		//
		// The lookup below is normalized TOO, and that is not cosmetic: it identifies an existing occupant by
		// name when the PMS sends no external guest id. Normalizing only the writes would make it stop
		// matching the rows already stored raw, and every sharer event would then INSERT a duplicate occupant
		// instead of updating one. Both sides move together or neither does.
		var id string
		err := tx.QueryRow(ctx, `SELECT id::text FROM iam_v2.stay_guests
			WHERE stay_id=$1 AND (
			      (COALESCE($2,'') <> '' AND external_guest_id = $2)
			   OR (COALESCE($2,'') = '' AND COALESCE(first_name_norm,'')=COALESCE(NULLIF($3,''),'')
			       AND COALESCE(last_name_norm,'')=COALESCE(NULLIF($4,''),'')))
			LIMIT 1`, stayID, s.ExternalGuestID,
			namenorm.Name(s.FirstName), namenorm.Name(s.LastName)).Scan(&id)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_guests
				(tenant_id, site_id, pms_interface_id, stay_id, external_guest_id, first_name_norm, last_name_norm, display_name, is_primary)
				VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),$9)`,
				tenant, site, iface, stayID, s.ExternalGuestID,
				namenorm.Name(s.FirstName), namenorm.Name(s.LastName), display, s.IsPrimary); err != nil {
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
				WHERE id=$5`, namenorm.Name(s.FirstName), namenorm.Name(s.LastName),
				display, s.IsPrimary, id); err != nil {
				return err
			}
		}
	}
	return nil
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
