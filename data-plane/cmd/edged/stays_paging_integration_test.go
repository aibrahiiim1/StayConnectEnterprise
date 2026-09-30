//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// THE STAYS SCREEN SEES THE WHOLE PROPERTY.
//
// It used to stop at 200 rows, and its counters counted those 200: a hotel with 403 in house was shown 200.
// These tests hold the server to paging every stay, counting over every match, and searching the whole list
// rather than the page on screen.

func (f *apiFixture) getWithSearch(t *testing.T, path, search string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest("GET", f.srv.URL+"/edge/v1"+path, bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	if search != "" {
		req.Header.Set(staySearchHeader, search)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.sessTok})
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// seedStays creates n checked-out stays on one interface. Every third is VIP and every fourth is booked
// through "Sunny Tours"; rooms are 1000+i so a search for one room is unambiguous.
func (f *apiFixture) seedStays(t *testing.T, n int) {
	t.Helper()
	ctx := context.Background()
	tag := fmt.Sprintf("P%d", time.Now().UnixNano())
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `WITH
		  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
		         VALUES (gen_random_uuid(),$1,$2,'protel-fias','ACTIVE') RETURNING id)
		INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,
		         status,lifecycle_version,last_applied_event_version,effective_checkout_at,
		         normalized_room_number,arrival,vip,travel_agent)
		SELECT gen_random_uuid(),$1,$2,pi.id,$3||i,$3||i,'CHECKED_OUT',1,0, now() - interval '1 hour',
		       (1000+i)::text, date '2026-09-01' + (i % 20),
		       CASE WHEN i % 3 = 0 THEN true END,
		       CASE WHEN i % 4 = 0 THEN 'Sunny Tours' END
		  FROM pi, generate_series(1,$4::int) i`, f.tenant, f.site, tag, n)
		return err
	}); err != nil {
		t.Fatalf("seed stays: %v", err)
	}
}

func summaryOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	s, ok := body["summary"].(map[string]any)
	if !ok {
		t.Fatalf("no summary in %v", body)
	}
	return s
}

func TestIntegration_Stays_PagedOverEveryStayWithTotalsOverEveryMatch(t *testing.T) {
	f := newAPI(t)
	f.seedStays(t, 230)

	seen := map[string]bool{}
	for page := 1; ; page++ {
		status, body := f.do(t, "GET", fmt.Sprintf("/pms-stays?status=CHECKED_OUT&page=%d&page_size=50", page), nil)
		if status != 200 {
			t.Fatalf("page %d = %d %v", page, status, body)
		}
		if got := summaryOf(t, body)["total"]; got != float64(230) {
			t.Fatalf("total = %v, want 230 on every page (the property, not the page)", got)
		}
		for _, r := range body["data"].([]any) {
			id := r.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatalf("stay %s listed on two pages", id)
			}
			seen[id] = true
		}
		if !body["meta"].(map[string]any)["has_more"].(bool) {
			break
		}
		if page > 10 {
			t.Fatal("paging never ended")
		}
	}
	if len(seen) != 230 {
		t.Fatalf("paged %d stays, want all 230 -- the old list stopped at 200", len(seen))
	}

	status, body := f.do(t, "GET", "/pms-stays?status=CHECKED_OUT&page_size=500", nil)
	if status != 400 {
		t.Fatalf("an oversized page must be refused, got %d %v", status, body)
	}
}

func TestIntegration_Stays_SearchCoversTheWholeListNotThePage(t *testing.T) {
	f := newAPI(t)
	f.seedStays(t, 230)

	// Room 1001 arrives earliest-but-one, so it sits past the first 200 by arrival order: the old in-browser
	// search could never have found it.
	status, body := f.getWithSearch(t, "/pms-stays?page_size=10", "1001")
	if status != 200 {
		t.Fatalf("search = %d %v", status, body)
	}
	if got := summaryOf(t, body)["total"]; got != float64(1) {
		t.Fatalf("search for room 1001 matched %v stays, want 1", got)
	}

	status, body = f.getWithSearch(t, "/pms-stays", "sunny")
	if status != 200 || summaryOf(t, body)["total"] != float64(57) {
		t.Fatalf("travel-agent search matched %v, want 57", summaryOf(t, body)["total"])
	}

	// % is text, not a wildcard.
	if _, body = f.getWithSearch(t, "/pms-stays", "%"); summaryOf(t, body)["total"] != float64(0) {
		t.Fatalf("a literal %% matched %v stays", summaryOf(t, body)["total"])
	}

	status, body = f.do(t, "GET", "/pms-stays?vip=true", nil)
	if status != 200 || summaryOf(t, body)["total"] != float64(76) || summaryOf(t, body)["vip"] != float64(76) {
		t.Fatalf("VIP filter = %v", summaryOf(t, body))
	}

	status, body = f.do(t, "GET", "/pms-stays?travel_agent=Sunny%20Tours", nil)
	if status != 200 || summaryOf(t, body)["total"] != float64(57) {
		t.Fatalf("travel-agent filter = %v", summaryOf(t, body))
	}
}

func TestIntegration_Stays_TravelAgentsListsWhatThePMSNamed(t *testing.T) {
	f := newAPI(t)
	f.seedStays(t, 20)
	status, body := f.do(t, "GET", "/pms-stays/travel-agents", nil)
	if status != 200 {
		t.Fatalf("travel agents = %d %v", status, body)
	}
	data := body["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("travel agents = %v, want exactly Sunny Tours", data)
	}
	a := data[0].(map[string]any)
	if a["name"] != "Sunny Tours" || a["stays"] != float64(5) || a["in_house"] != float64(0) {
		t.Fatalf("travel agent row = %v", a)
	}
}
