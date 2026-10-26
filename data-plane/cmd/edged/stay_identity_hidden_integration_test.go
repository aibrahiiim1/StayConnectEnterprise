//go:build integration

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// A reservation number, with the room the room sign-in credential, is hidden from roles without pms-stay-identity on the PMS reconciliation lists.
func TestIntegration_ReconciliationHidesReservationNumbersFromTheViewer(t *testing.T) {
	f := newAPI(t, "site_viewer")
	res := "R-PAST-" + fmt.Sprint(time.Now().UnixNano())
	stay := seedGuestDataStay(t, f, res, "IN_HOUSE")
	if err := controlled(context.Background(), f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE iam_v2.stays SET arrival = current_date - 6, departure = current_date - 3 WHERE id=$1::uuid`, stay)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var listed int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM iam_v2.pms_stays_past_departure WHERE stay_id=$1::uuid AND reservation=$2`, stay, res).Scan(&listed); err != nil || listed != 1 {
		t.Fatalf("the stay must be past departure with its reservation (%d, %v)", listed, err)
	}
	st, raw := f.doRaw(t, "GET", "/pms-reconciliation/past-departure", nil)
	if st != 200 || strings.Contains(raw, `"reservation":`) || !strings.Contains(raw, `"identity_hidden":true`) || !strings.Contains(raw, stay) {
		t.Fatalf("the viewer must see the stay without its reservation: %d %s", st, raw)
	}
	if got := opaqueCaseKey("reservation:" + res); strings.Contains(got, res) || got != opaqueCaseKey("reservation:"+res) {
		t.Fatalf("the case key must be a stable digest: %q", got)
	}
}
