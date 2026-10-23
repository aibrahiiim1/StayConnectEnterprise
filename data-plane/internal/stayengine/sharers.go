package stayengine

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/namenorm"
)

// Sharer is one occupant of a Stay as the PMS reports it; exactly one is the primary. The external id is
// interface-scoped.
type Sharer struct {
	ExternalGuestID string `json:"external_guest_id"`
	FirstName       string `json:"first_name"`
	LastName        string `json:"last_name"`
	IsPrimary       bool   `json:"is_primary"`
}

// ErrSourceConflict: the occupancy facts cannot be applied without overwriting another Stay's facts or
// guessing between contradictory ones; routed to MANUAL_REVIEW.
var ErrSourceConflict = errors.New("stayengine: source conflict")

const (
	CodeSharerDuplicate  = "SHARER_DUPLICATE_IDENTITY"
	CodeSharerTwoPrimary = "SHARER_MULTIPLE_PRIMARY"
)

// validateSharers rejects a self-contradicting payload (duplicate guest, two primaries) before any write.
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

// applySharers reconciles the Stay's occupants, keyed by external guest id or, when absent, normalized name.
// The primary flag is moved (demote first) because the one_primary_guest_per_stay unique index would
// abort the event on a second primary.
func applySharers(ctx context.Context, tx pgx.Tx, tenant, site, iface, stayID string, sharers []Sharer) error {
	for _, s := range sharers {
		display := displayName(s.FirstName, s.LastName)
		if s.IsPrimary {
			if _, err := tx.Exec(ctx, `UPDATE iam_v2.stay_guests SET is_primary=false
				WHERE stay_id=$1 AND is_primary AND COALESCE(external_guest_id,'') <> $2`, stayID, s.ExternalGuestID); err != nil {
				return err
			}
		}
		// The `_norm` writes and the by-name lookup must both be normalized, or a re-sent occupant is
		// inserted as a duplicate instead of updated.
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
