package main

// Which customer's data this appliance still holds. Changing customer goes retire -> factory-clean -> new activation; an appliance holding
// customer A's data must never be activated for B. An identity reset keeps the site database, so every registration carries holds_customer_id
// inside the signed body, and Central refuses activation for any other customer (409 holds_other_customer_data).
// Sources: public.tenants (mirrored on adoption and every boot) and a granting or terminal assignment still on disk.
// More than one customer means a cross-customer transition never completed: the appliance does not register until factory-reset.

import (
	"context"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
)

// errHoldsSeveralCustomers refuses registration; its text is shown in Hotel Admin as the last Central error.
var errHoldsSeveralCustomers = errors.New("this appliance holds local data of more than one customer; " +
	"it will not register with OneGate Central until it is factory-reset")

// localHeldCustomers returns every customer id whose data this appliance holds (site database tenants and any granting or terminal
// assignment on disk); an error means cannot tell and the caller must not register.
func localHeldCustomers(ctx context.Context, db *pgxpool.Pool, assignmentDir string) ([]string, error) {
	seen := map[string]bool{}
	if assignmentDir != "" {
		if rec, err := (&assignment.Store{Dir: assignmentDir}).Load(); err == nil && rec != nil {
			for _, d := range []*assignment.Document{rec.Current, rec.Previous} {
				if d != nil && d.TenantID != "" && (assignment.Grants(d.State) || assignment.Clears(d.State)) {
					seen[d.TenantID] = true
				}
			}
		}
	}
	if db != nil {
		rows, err := db.Query(ctx, `SELECT id::text FROM tenants`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			seen[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func singleHeldCustomer(ids []string) (string, error) {
	switch len(ids) {
	case 0:
		return "", nil
	case 1:
		return ids[0], nil
	default:
		return "", errHoldsSeveralCustomers
	}
}

func heldCustomerID(ctx context.Context, db *pgxpool.Pool, assignmentDir string) (string, error) {
	ids, err := localHeldCustomers(ctx, db, assignmentDir)
	if err != nil {
		return "", err
	}
	return singleHeldCustomer(ids)
}
