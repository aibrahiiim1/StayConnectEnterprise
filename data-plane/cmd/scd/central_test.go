package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/appliancecert"
	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/identity"
	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	lic "github.com/stayconnect/enterprise/license"
)

var now0 = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

func granted() assignment.Resolution {
	return assignment.Resolution{Outcome: assignment.OutcomeGranted, TenantID: "t", SiteID: "s", State: "assigned", Version: 3}
}

func activeLic(validFor time.Duration) licSnapshot {
	return licSnapshot{Installed: true, State: lic.StateActive, ValidUntil: now0.Add(validFor),
		GraceEnds: now0.Add(validFor + 14*24*time.Hour), MaxGuests: 50, Version: 2, LicenseID: "L1"}
}

func connected(in centralInputs) centralInputs {
	in.CentralConfigured = true
	in.LastOK = now0.Add(-time.Minute)
	return in
}

// The section 8 computation, over activation x licence x connection.
func TestComputeCentralStatus(t *testing.T) {
	base := func() centralInputs {
		return centralInputs{Now: now0, ApplianceID: "appl-1", Serial: "SC-1", CertRequired: true, CertReady: true,
			CustomerName: "Coral Sea", SiteName: "Aqua"}
	}
	cases := []struct {
		name       string
		in         centralInputs
		activation string
		license    string
		central    string
		daysLeft   *int
	}{
		{"factory clean", centralInputs{Now: now0, CentralConfigured: true}, activationNotRegistered, licNone, centralUnreachable, nil},
		{"registered, nothing assigned", connected(func() centralInputs { i := base(); i.Asg.Outcome = assignment.OutcomeAbsent; return i }()),
			activationWaiting, licNone, centralConnected, nil},
		{"refused assignment is waiting, not activated", connected(func() centralInputs {
			i := base()
			i.Asg.Outcome = assignment.OutcomeUnverifiable
			i.Lic = activeLic(200 * 24 * time.Hour)
			return i
		}()), activationWaiting, licActive, centralConnected, nil},
		{"returned to inventory is waiting", connected(func() centralInputs {
			i := base()
			i.Asg = assignment.Resolution{Outcome: assignment.OutcomeNotGranting, State: assignment.StateUnassigned}
			return i
		}()), activationWaiting, licNone, centralConnected, nil},
		{"assigned, licence not collected", connected(func() centralInputs { i := base(); i.Asg = granted(); return i }()),
			activationActivating, licNone, centralConnected, nil},
		{"licence in, certificate still being collected", connected(func() centralInputs {
			i := base()
			i.Asg, i.Lic, i.CertReady = granted(), activeLic(200*24*time.Hour), false
			return i
		}()), activationActivating, licActive, centralConnected, nil},
		{"offline-activated without a certificate and no Central is activated", func() centralInputs {
			i := base()
			i.Asg, i.Lic, i.CertReady, i.CentralConfigured = granted(), activeLic(200*24*time.Hour), false, true
			i.LastFail, i.LastErr = now0, "Could not reach OneGate Central."
			return i
		}(), activationActivated, licActive, centralUnreachable, nil},
		{"fully activated", connected(func() centralInputs { i := base(); i.Asg, i.Lic = granted(), activeLic(200*24*time.Hour); return i }()),
			activationActivated, licActive, centralConnected, intp(200)},
		{"expiring within 30 days", connected(func() centralInputs { i := base(); i.Asg, i.Lic = granted(), activeLic(10*24*time.Hour); return i }()),
			activationActivated, licExpiring, centralConnected, intp(10)},
		{"grace", connected(func() centralInputs {
			i := base()
			i.Asg, i.Lic = granted(), activeLic(-2*24*time.Hour)
			i.Lic.State = lic.StateGracePeriod
			return i
		}()), activationActivated, licGrace, centralConnected, intp(12)},
		{"expired", connected(func() centralInputs {
			i := base()
			i.Asg, i.Lic = granted(), activeLic(-40*24*time.Hour)
			i.Lic.State = lic.StateExpired
			return i
		}()), activationActivated, licExpired, centralConnected, intp(0)},
		{"suspended", connected(func() centralInputs {
			i := base()
			i.Asg, i.Lic = granted(), activeLic(90*24*time.Hour)
			i.Lic.State = lic.StateSuspended
			return i
		}()),
			activationActivated, licSuspended, centralConnected, nil},
		{"revoked", connected(func() centralInputs {
			i := base()
			i.Asg, i.Lic = granted(), activeLic(90*24*time.Hour)
			i.Lic.State = lic.StateRevoked
			return i
		}()),
			activationActivated, licRevoked, centralConnected, nil},
		{"wrong hardware", connected(func() centralInputs {
			i := base()
			i.Asg, i.Lic = granted(), activeLic(90*24*time.Hour)
			i.Lic.State, i.Lic.WrongHardware = lic.StateUnlicensed, "identity key fingerprint mismatch"
			return i
		}()), activationActivated, licWrongHardware, centralConnected, nil},
		{"retired", connected(func() centralInputs {
			i := base()
			i.Asg = assignment.Resolution{Outcome: assignment.OutcomeNotGranting, State: assignment.StateDecommissioned, Version: 9}
			return i
		}()), activationRetired, licNone, centralConnected, nil},
		{"not configured", func() centralInputs { i := base(); i.Asg, i.Lic = granted(), activeLic(90*24*time.Hour); return i }(),
			activationActivated, licActive, centralNotConfigured, intp(90)},
		{"stale contact is unreachable", func() centralInputs {
			i := base()
			i.Asg, i.Lic, i.CentralConfigured = granted(), activeLic(90*24*time.Hour), true
			i.LastOK = now0.Add(-20 * time.Minute)
			return i
		}(), activationActivated, licActive, centralUnreachable, intp(90)},
		{"failure after success is unreachable", func() centralInputs {
			i := base()
			i.Asg, i.Lic, i.CentralConfigured = granted(), activeLic(90*24*time.Hour), true
			i.LastOK, i.LastFail, i.LastErr = now0.Add(-2*time.Minute), now0.Add(-time.Minute), "OneGate Central did not answer in time."
			return i
		}(), activationActivated, licActive, centralUnreachable, intp(90)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := computeCentralStatus(c.in)
			if got.Activation != c.activation || got.License.State != c.license || got.Central.State != c.central {
				t.Fatalf("got activation=%s licence=%s central=%s; want %s/%s/%s",
					got.Activation, got.License.State, got.Central.State, c.activation, c.license, c.central)
			}
			if c.daysLeft != nil && (got.License.DaysLeft == nil || *got.License.DaysLeft != *c.daysLeft) {
				t.Fatalf("days_left = %v, want %d", got.License.DaysLeft, *c.daysLeft)
			}
			if got.Central.State == centralUnreachable && got.Central.LastContactAt != nil && got.Central.LastError == nil {
				t.Fatal("an unreachable Central with a past contact must say why")
			}
			// Customer and site are named only once the verified assignment grants them.
			named := got.CustomerName != nil
			if named != (c.activation == activationActivated || c.activation == activationActivating) {
				t.Fatalf("customer_name shown=%v for activation %s", named, got.Activation)
			}
		})
	}
}

func intp(i int) *int { return &i }

// The JSON is the section 8 contract: every documented key present, nulls where unknown.
func TestCentralStatusJSONShape(t *testing.T) {
	b, _ := json.Marshal(computeCentralStatus(centralInputs{Now: now0}))
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"activation", "serial", "appliance_id", "customer_name", "site_name", "license", "central", "details"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing top-level key %q", k)
		}
	}
	licm := m["license"].(map[string]any)
	for _, k := range []string{"state", "valid_until", "grace_ends_at", "days_left", "max_concurrent_online_guests", "current_online_guests"} {
		if _, ok := licm[k]; !ok {
			t.Errorf("missing license.%s", k)
		}
	}
	cm := m["central"].(map[string]any)
	for _, k := range []string{"state", "last_contact_at", "last_error"} {
		if _, ok := cm[k]; !ok {
			t.Errorf("missing central.%s", k)
		}
	}
	dm := m["details"].(map[string]any)
	for _, k := range []string{"identity_key_fingerprint", "cert_fingerprint", "cert_not_after", "assignment_version",
		"license_version", "wan_mac", "lan_mac", "central_endpoint"} {
		if _, ok := dm[k]; !ok {
			t.Errorf("missing details.%s", k)
		}
	}
}

func TestCentralContactRecordsAnswersAndFailures(t *testing.T) {
	c := &centralContact{}
	c.record(licstate.ErrNoLicenseYet, now0)
	c.record(appliancecert.ErrCertPending, now0.Add(time.Second))
	ok, fail, _ := c.snapshot()
	if !ok.Equal(now0.Add(time.Second)) || !fail.IsZero() {
		t.Fatal("\"nothing for you yet\" answers are successful contact")
	}
	c.record(&net.DNSError{Err: "no such host", Name: "central"}, now0.Add(2*time.Second))
	_, fail, msg := c.snapshot()
	if fail.IsZero() || !strings.Contains(msg, "DNS") {
		t.Fatalf("DNS failure must be recorded in words, got %q", msg)
	}
	for err, want := range map[error]string{
		&identity.StatusError{Code: 403}:           "credentials",
		&licstate.HTTPError{Code: 503}:             "its side",
		context.DeadlineExceeded:                   "in time",
		errors.New("x509: unknown authority"):      "trust",
		errors.New("dial tcp: connection refused"): "refused",
	} {
		if got := describeCentralErr(err); !strings.Contains(got, want) {
			t.Errorf("%v -> %q, want it to mention %q", err, got, want)
		}
	}
}
