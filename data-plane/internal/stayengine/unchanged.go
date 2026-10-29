package stayengine

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/namenorm"
)

// fastPathApplied counts events applied through the cheap path below; tests and the progress log read it.
var fastPathApplied atomic.Int64

// FastPathApplied reports how many roster/GI/GC records were applied without rewriting an unchanged stay.
func FastPathApplied() int64 { return fastPathApplied.Load() }

// recordUnchanged answers whether applying ev to the stay would change nothing: same arrival and departure when the record
// states them, same VIP and travel agent when stated, and every named occupant already mirrored with the same normalised names
// and the same primary flag. It is deliberately conservative: any doubt is "changed", and the full apply runs.
//
// This is the cheap path of audit AD-37 (D6-H1 step 2). Every connection starts with a full resync, so a roster of N in-house
// guests becomes N events that mostly change nothing; the full apply costs 17 to 20 ms each. Skipping the writes that would
// be no-ops keeps the two things the room sign-in feed rule reads: the stay's occupancy evidence is re-stamped (freshness) and
// the event is APPLIED with the stay's last_applied_event_id pinned (lineage). Nothing is marked SKIPPED_DUPLICATE, because a
// skipped record would not refresh the evidence that proves the guest is still in the house.
func recordUnchanged(ctx context.Context, tx pgx.Tx, tenant, site, stayID string, ev InboxEvent) (bool, error) {
	arrival, departure := parseYYMMDD(ev.ArrivalRaw), parseYYMMDD(ev.DepartureRaw)
	var agent any
	if ev.TravelAgent != nil {
		agent = *ev.TravelAgent
	}
	var vip any
	if ev.VIP != nil {
		vip = *ev.VIP
	}
	var same bool
	if err := tx.QueryRow(ctx, `SELECT
		   ($2::date IS NULL OR arrival = $2::date)
		   AND ($3::date IS NULL OR departure = $3::date)
		   AND ($4::text IS NULL OR COALESCE(travel_agent,'') = $4::text)
		   AND ($5::bool IS NULL OR vip IS NOT DISTINCT FROM $5::bool)
		FROM iam_v2.stays WHERE id=$1 AND tenant_id=$6 AND site_id=$7`, stayID, arrival, departure, agent, vip, tenant, site).Scan(&same); err != nil {
		return false, err
	}
	if !same {
		return false, nil
	}

	type guest struct {
		external, first, last, display string
		primary                        bool
	}
	rows, err := tx.Query(ctx, `SELECT COALESCE(external_guest_id,''), COALESCE(first_name_norm,''), COALESCE(last_name_norm,''),
		COALESCE(display_name,''), is_primary FROM iam_v2.stay_guests WHERE stay_id=$1 AND tenant_id=$2 AND site_id=$3`, stayID, tenant, site)
	if err != nil {
		return false, err
	}
	var have []guest
	for rows.Next() {
		var g guest
		if err := rows.Scan(&g.external, &g.first, &g.last, &g.display, &g.primary); err != nil {
			rows.Close()
			return false, err
		}
		have = append(have, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	if len(ev.Sharers) == 0 {
		if ev.FirstName == "" && ev.LastName == "" {
			return true, nil
		}
		first, last := namenorm.Name(ev.FirstName), namenorm.Name(ev.LastName)
		display := displayName(ev.FirstName, ev.LastName)
		for _, g := range have {
			if g.primary {
				return g.first == first && g.last == last && g.display == display, nil
			}
		}
		return false, nil // no primary row yet: the full path inserts it
	}

	for _, s := range ev.Sharers {
		first, last := namenorm.Name(s.FirstName), namenorm.Name(s.LastName)
		display := displayName(s.FirstName, s.LastName)
		ext := strings.TrimSpace(s.ExternalGuestID)
		if ext != s.ExternalGuestID {
			return false, nil // an untrimmed id is matched exactly by the full path; do not reason about it
		}
		var row *guest
		for i := range have {
			g := &have[i]
			if ext != "" {
				if g.external == ext {
					row = g
					break
				}
			} else if g.first == first && g.last == last {
				row = g
				break
			}
		}
		if row == nil {
			return false, nil
		}
		// The update only fills a value the record states and never blanks one.
		if (first != "" && row.first != first) || (last != "" && row.last != last) || (display != "" && row.display != display) {
			return false, nil
		}
		if s.IsPrimary {
			if !row.primary {
				return false, nil
			}
			for _, g := range have {
				if g.primary && g.external != s.ExternalGuestID {
					return false, nil // the full path would demote another primary
				}
			}
		}
	}
	return true, nil
}
