package main

// THE READ-ONLY ADDITIONS TO THE VOUCHER SURFACE: the batch list and one card's history.
//
// Both sit under `vouchers`, the daily key, because neither returns code material -- a batch is a count of
// cards and a history is when a card was issued, redeemed or cancelled. What these pin is that placement,
// in both directions: every role that may read the card list may read them, no role gains anything that
// reads a code, and neither route accepts a write.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestTheBatchListAndHistoryAreRegisteredAsReadsOnly(t *testing.T) {
	s := &server{}
	want := map[string]bool{"GET /batches": false, "GET /{id}/history": false}
	err := chi.Walk(s.vouchersRoutes().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := method + " " + strings.TrimSuffix(route, "/")
		if _, ok := want[key]; ok {
			want[key] = true
		}
		if method != http.MethodGet && (strings.Contains(route, "batches") || strings.Contains(route, "history")) {
			t.Errorf("%s %s: the batch list and the card history are reads; nothing may write through them", method, route)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for k, found := range want {
		if !found {
			t.Errorf("%s is not registered on the vouchers surface", k)
		}
	}
}

func TestTheBatchListAndHistoryFollowTheCardListPermission(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	reached := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		mountResource(r, s, "vouchers", func() http.Handler {
			rr := chi.NewRouter()
			rr.Get("/", reached)
			rr.Get("/batches", reached)
			rr.Get("/{id}/history", reached)
			return rr
		})
	})

	for _, c := range []struct {
		role string
		want int
	}{
		{"site_admin", http.StatusTeapot},
		{"hotel_it_manager", http.StatusTeapot},
		{"front_office_operator", http.StatusTeapot},
		{"voucher_operator", http.StatusTeapot},
		// Reads the list, so reads the batches: a count of cards is not a code.
		{"payments_operator", http.StatusTeapot},
		{"site_viewer", http.StatusTeapot},
	} {
		cookie := loginAs(t, s, []string{c.role})
		for _, p := range []string{"/vouchers/", "/vouchers/batches", "/vouchers/11111111-1111-4111-8111-111111111111/history"} {
			if got := protectionDo(t, r, http.MethodGet, p, cookie); got != c.want {
				t.Errorf("%s: GET %s = %d, want %d", c.role, p, got, c.want)
			}
		}
	}
	if got := protectionDo(t, r, http.MethodGet, "/vouchers/batches", ""); got == http.StatusTeapot {
		t.Error("the batch list was reachable without a session")
	}
}

// THE LABELS ARE ADDITIVE. Every field scd sent survives, the names are added beside the ids, and a row the
// lookup could not name is left exactly as it was rather than dropped.
func TestVoucherLabelsAreAddedWithoutChangingWhatScdSent(t *testing.T) {
	raw := []byte(`{"authority":"iam_v2","total":3,"vouchers":[
	  {"id":"v1","code_last4":"AB12","package_revision_id":"r1","issued_by":"o1","state":"UNUSED"},
	  {"id":"v2","code_last4":"CD34","package_revision_id":"r-unknown","issued_by":null,"state":"REDEEMED"}]}`)
	revs, ops := voucherRowIDs(raw, "vouchers")
	if len(revs) != 2 || len(ops) != 1 {
		t.Fatalf("ids collected: revisions %v operators %v", revs, ops)
	}
	out := applyVoucherLabels(raw, "vouchers",
		map[string]voucherPackageLabel{"r1": {Name: "One day", Code: "DAY", RevisionNo: 3}},
		map[string]string{"o1": "Front desk"})
	var got struct {
		Authority string           `json:"authority"`
		Total     int              `json:"total"`
		Vouchers  []map[string]any `json:"vouchers"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Authority != "iam_v2" || got.Total != 3 || len(got.Vouchers) != 2 {
		t.Fatalf("the envelope changed: %s", out)
	}
	a, b := got.Vouchers[0], got.Vouchers[1]
	if a["package_name"] != "One day" || a["package_code"] != "DAY" || a["package_revision_no"] != float64(3) {
		t.Errorf("package label missing: %v", a)
	}
	if a["issued_by_label"] != "Front desk" || a["code_last4"] != "AB12" || a["state"] != "UNUSED" {
		t.Errorf("operator label missing or scd fields lost: %v", a)
	}
	if _, has := b["package_name"]; has {
		t.Errorf("an unknown revision was given a name: %v", b)
	}
	if b["code_last4"] != "CD34" {
		t.Errorf("an unnamed row lost its fields: %v", b)
	}
	// Nothing to add, or nothing parseable: relayed byte for byte.
	if string(applyVoucherLabels(raw, "vouchers", nil, nil)) != string(raw) {
		t.Error("with no labels the response must be relayed untouched")
	}
	if string(applyVoucherLabels([]byte("not json"), "vouchers", map[string]voucherPackageLabel{"r1": {}}, nil)) != "not json" {
		t.Error("an unparseable response must be relayed untouched")
	}
}
