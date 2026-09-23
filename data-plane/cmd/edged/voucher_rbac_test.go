package main

// AUTHORIZATION for the three voucher keys, through the ACTUAL router and session middleware.
//
// The tempting shape is one "vouchers" key. It would be wrong for the same reason the guest sign-in keys
// were split, and the reason is sharper here:
//
//   * vouchers               print a batch, read the list, cancel a lost card. The daily job.
//   * voucher-codes          recover a code IN THE CLEAR for a card already in a guest's hand. Additionally
//                            gated by a password step-up and a mandatory bounded reason at the route, and
//                            it writes an append-only row naming the operator every time.
//   * voucher-code-settings  choose what a code looks like -- digits or mixed, and how long. A property
//                            configuration decision, which is why it sits with auth-methods' owner.
//
// WHY THE SECOND KEY IS NOT REDUNDANT, given that issuing already returns plaintext: issuing creates codes
// nobody holds yet, while revealing reads codes already printed -- possibly from a batch another operator
// printed and handed out. A role with issue and no reveal genuinely cannot read somebody else's batch.
//
// hotel_it_manager is the case worth stating: it holds vouchers WRITE and voucher-code-settings WRITE and
// is DENIED voucher-codes. That role owns configuration and the PMS integration; neither job requires
// reading a guest's credential, and configuring the format is not reading a code.

import (
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func voucherRBACRouter(s *server) http.Handler {
	reached := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		mountResource(r, s, "vouchers", func() http.Handler {
			rr := chi.NewRouter()
			rr.Get("/", reached)
			rr.Post("/issue", reached)
			rr.Post("/{id}/revoke", reached)
			return rr
		})
		mountResource(r, s, "voucher-codes", func() http.Handler {
			rr := chi.NewRouter()
			rr.Get("/reveals", reached)
			rr.Post("/{id}/reveal", reached)
			rr.Post("/export", reached)
			return rr
		})
		mountResource(r, s, "voucher-code-settings", func() http.Handler {
			rr := chi.NewRouter()
			rr.Get("/", reached)
			rr.Put("/", reached)
			return rr
		})
	})
	return r
}

func TestVoucherSurface_PermissionModel(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	h := voucherRBACRouter(s)

	const reach = http.StatusTeapot
	const deny = http.StatusForbidden

	cases := []struct {
		role                    string
		listCards, printCards   int
		readReveals, revealCode int
		readFormat, setFormat   int
	}{
		{"site_admin", reach, reach, reach, reach, reach, reach},
		// Owns configuration and the integration. Prints cards, sets the format, reads no code.
		{"hotel_it_manager", reach, reach, deny, deny, reach, reach},
		// The desk: the guest with the unreadable card is standing there.
		{"front_office_operator", reach, reach, reach, reach, reach, deny},
		{"guest_relations_operator", reach, reach, reach, reach, reach, deny},
		// The role NAMED for this, which until now held no voucher permission at all.
		{"voucher_operator", reach, reach, reach, reach, reach, deny},
		// A voucher is a commercial instrument, so this role reads the list -- and no code, and no format.
		{"payments_operator", reach, deny, deny, deny, deny, deny},
		// Read-only everywhere, and no guest secrets: the same line drawn for guest-signin-credentials.
		{"site_viewer", reach, deny, deny, deny, reach, deny},
	}

	for _, c := range cases {
		cookie := loginAs(t, s, []string{c.role})
		checks := []struct {
			label, method, path string
			want                int
		}{
			{"read the card list", http.MethodGet, "/vouchers/", c.listCards},
			{"print a batch", http.MethodPost, "/vouchers/issue", c.printCards},
			{"cancel a card", http.MethodPost, "/vouchers/abc/revoke", c.printCards},
			{"read who has revealed", http.MethodGet, "/voucher-codes/reveals", c.readReveals},
			{"reveal one code", http.MethodPost, "/voucher-codes/abc/reveal", c.revealCode},
			{"export a batch of codes", http.MethodPost, "/voucher-codes/export", c.revealCode},
			{"read the code format", http.MethodGet, "/voucher-code-settings/", c.readFormat},
			{"change the code format", http.MethodPut, "/voucher-code-settings/", c.setFormat},
		}
		for _, k := range checks {
			if got := protectionDo(t, h, k.method, k.path, cookie); got != k.want {
				t.Errorf("%s: %s = %d, want %d", c.role, k.label, got, k.want)
			}
		}
	}
}

// PRINTING A CARD DOES NOT CARRY PERMISSION TO READ ONE ALREADY PRINTED.
//
// Stated separately because the two keys are held by an overlapping set of roles, which is exactly the
// situation in which somebody later decides one implies the other and merges them.
func TestPrintingDoesNotCarryRevealing(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	h := voucherRBACRouter(s)

	cookie := loginAs(t, s, []string{"hotel_it_manager"})
	if got := protectionDo(t, h, http.MethodPost, "/vouchers/issue", cookie); got != http.StatusTeapot {
		t.Fatalf("setup: the IT manager should be able to print a batch, got %d", got)
	}
	if got := protectionDo(t, h, http.MethodPost, "/voucher-codes/abc/reveal", cookie); got != http.StatusForbidden {
		t.Fatalf("printing a batch carried permission to read a code already in circulation: %d", got)
	}
	if got := protectionDo(t, h, http.MethodGet, "/voucher-codes/reveals", cookie); got != http.StatusForbidden {
		t.Fatalf("a role denied the reveal can still read WHO revealed: %d", got)
	}
}

// AND SETTING THE FORMAT DOES NOT CARRY READING A CODE, which is the other direction of the same mistake:
// the screen shows the format panel and the reveal button side by side, so the two are easy to conflate in
// a later refactor of the route file.
func TestChoosingTheFormatDoesNotCarryReadingACode(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	h := voucherRBACRouter(s)

	cookie := loginAs(t, s, []string{"hotel_it_manager"})
	if got := protectionDo(t, h, http.MethodPut, "/voucher-code-settings/", cookie); got != http.StatusTeapot {
		t.Fatalf("setup: the IT manager owns the code format, got %d", got)
	}
	if got := protectionDo(t, h, http.MethodPost, "/voucher-codes/export", cookie); got != http.StatusForbidden {
		t.Fatalf("choosing what codes look like carried permission to read them: %d", got)
	}
}

// A DESK ROLE MAY NOT RE-TUNE THE PROPERTY, which is the same line the sign-in protection policy draws: the
// desk releases one restriction and does not change the threshold for everybody. Here the desk prints and
// reveals and cannot make every future code six digits because a guest complained about typing.
func TestTheDeskDoesNotChooseTheFormat(t *testing.T) {
	s := &server{sessions: newSessionStore(time.Hour)}
	h := voucherRBACRouter(s)

	for _, role := range []string{"front_office_operator", "guest_relations_operator", "voucher_operator"} {
		cookie := loginAs(t, s, []string{role})
		if got := protectionDo(t, h, http.MethodGet, "/voucher-code-settings/", cookie); got != http.StatusTeapot {
			t.Errorf("%s should see what the format is, got %d", role, got)
		}
		if got := protectionDo(t, h, http.MethodPut, "/voucher-code-settings/", cookie); got != http.StatusForbidden {
			t.Errorf("%s could change the property's code format: %d", role, got)
		}
	}
}
