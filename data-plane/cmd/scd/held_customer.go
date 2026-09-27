package main

// WHICH CUSTOMER'S DATA THIS APPLIANCE STILL HOLDS.
//
// Product-Owner rule: changing an appliance's customer goes through retire -> FACTORY-CLEAN -> new activation.
// No path may let an appliance that still holds customer A's local data be activated for customer B.
//
// The identity key is not that proof. A support identity reset (docs/VENDOR_BREAKGLASS_RUNBOOK.md) wipes
// /etc/stayconnect/identity but keeps the site database, and scd then generates a new key and registers
// token-less -- to Central indistinguishable from a factory-clean box. So every registration (online and the
// offline activation request) carries `holds_customer_id`: the customer whose data the local site database
// still holds. It is inside the signed body, so it is covered by the identity key's signature, and Central
// refuses to activate the appliance for any other customer (control-plane activate, 409
// holds_other_customer_data). A factory-clean appliance holds none and omits the field.
//
// Sources, both authoritative for "this box has held that customer":
//   - public.tenants: every tenant-scoped row on the appliance references it by foreign key, and scd mirrors
//     the assigned tenant into it on adoption and on every boot (seedTenantSiteMirror). No migration seeds it.
//   - a granting or terminal signed assignment still on disk (current or previous), which survives a wiped
//     database.
//
// More than one customer means a cross-customer transition never completed (the purge is gated and fails
// closed). That appliance cannot be described by one customer, so it does not register at all until it is
// factory-reset; the refusal is recorded as the last Central error, which Hotel Admin shows.

import (
	"context"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
)

// errHoldsSeveralCustomers refuses registration for an appliance whose local data belongs to more than one
// customer. The text is what Hotel Admin shows as the last Central error.
var errHoldsSeveralCustomers = errors.New("this appliance holds local data of more than one customer; " +
	"it will not register with OneGate Central until it is factory-reset")

// localHeldCustomers returns, sorted and de-duplicated, every customer (tenant) id whose data this appliance
// holds: the site database's tenants and the tenant of any granting or terminal assignment on disk. An error
// means "cannot tell"; the caller must not register on it.
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

// singleHeldCustomer reduces the held set to what a registration may carry: "" for none, the id for one,
// errHoldsSeveralCustomers for more.
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

// heldCustomerID is the value of holds_customer_id for this appliance right now.
func heldCustomerID(ctx context.Context, db *pgxpool.Pool, assignmentDir string) (string, error) {
	ids, err := localHeldCustomers(ctx, db, assignmentDir)
	if err != nil {
		return "", err
	}
	return singleHeldCustomer(ids)
}
