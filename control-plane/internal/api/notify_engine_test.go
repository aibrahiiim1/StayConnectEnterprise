package api

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func notifySettings() notificationSettings {
	return notificationSettings{FirstWarningDays: 30, SecondWarningDays: 7, FinalWarningDays: 1, OfflineAfterMinutes: 60,
		DigestHourUTC: 7, MaxAttempts: 8}
}

func TestLicenceStage(t *testing.T) {
	s := notifySettings()
	day := 24 * time.Hour
	for _, c := range []struct {
		left time.Duration
		want int
	}{{40 * day, 0}, {30 * day, 1}, {8 * day, 1}, {7 * day, 2}, {2 * day, 2}, {day, 3}, {time.Hour, 3}} {
		if got := licenceStage(s, c.left); got != c.want {
			t.Errorf("%v left: stage %d, want %d", c.left, got, c.want)
		}
	}
}

func TestApplianceNotices(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	until := now.Add(5 * 24 * time.Hour)
	stale := now.Add(-2 * time.Hour)
	fresh := now.Add(-time.Minute)
	cust, site := "Hotel Group", "Cairo"
	apps := []ApplianceRow{
		{ID: "a1", Serial: "SN1", Activation: ActivationActivated, LastSeenAt: &stale, CustomerName: &cust, SiteName: &site,
			License: LicenseSummary{State: LicenseExpiring, ValidUntil: &until}},
		{ID: "a2", Serial: "SN2", Activation: ActivationActivated, LastSeenAt: &fresh, License: LicenseSummary{State: LicenseActive}},
		{ID: "a3", Serial: "SN3", Activation: ActivationWaiting, RegisteredAt: now},
		{ID: "a4", Serial: "SN4", Activation: ActivationRetired, LastSeenAt: &stale},
		{ID: "a5", Serial: "SN5", Activation: ActivationRetiring, LastSeenAt: &fresh, terminalDelivery: "terminal_delivery_failed"},
	}
	got := map[string]notice{}
	for _, n := range applianceNotices(notifySettings(), apps, now) {
		got[n.Kind+"/"+n.Subject] = n
	}
	exp := got["licence_expiring/a1|"+strconv.FormatInt(until.Unix(), 10)]
	if exp.Stage != 2 || !strings.Contains(exp.Title, "ends in 5 days") || !strings.Contains(exp.Body, "customer Hotel Group, site Cairo") {
		t.Errorf("licence notice: %+v", exp)
	}
	if _, ok := got["appliance_offline/a1"]; !ok {
		t.Error("a stale activated appliance was not reported offline")
	}
	if _, ok := got["appliance_offline/a2"]; ok {
		t.Error("a fresh appliance was reported offline")
	}
	if _, ok := got["appliance_waiting/a3|waiting"]; !ok {
		t.Error("a waiting appliance was not reported")
	}
	for k := range got {
		if strings.Contains(k, "/a4") {
			t.Errorf("a retired appliance produced %s", k)
		}
	}
	if _, ok := got["retirement_unconfirmed/a5"]; !ok {
		t.Error("an unconfirmed retirement was not reported")
	}
	for _, n := range got {
		if strings.Contains(n.Title+n.Body, "—") {
			t.Errorf("an em dash in %q", n.Title)
		}
	}
}

func TestBackOnlineOnlyWhenReallyBack(t *testing.T) {
	now := time.Now()
	recent, old := now.Add(-time.Minute), now.Add(-time.Hour)
	apps := map[string]ApplianceRow{
		"back":  {ID: "back", Serial: "SN1", Activation: ActivationActivated, LastSeenAt: &recent},
		"still": {ID: "still", Serial: "SN2", Activation: ActivationActivated, LastSeenAt: &old},
	}
	if _, ok := backOnlineNotice(apps, "back", now); !ok {
		t.Error("an appliance seen a minute ago was not reported back")
	}
	if _, ok := backOnlineNotice(apps, "still", now); ok {
		t.Error("an appliance silent for an hour was reported back")
	}
	if _, ok := backOnlineNotice(apps, "gone", now); ok {
		t.Error("a deleted appliance was reported back")
	}
}

func TestDigestDue(t *testing.T) {
	s := notifySettings()
	at := func(h int) time.Time { return time.Date(2026, 11, 5, h, 30, 0, 0, time.UTC) }
	if !digestDue(s, at(8)) {
		t.Error("never sent: due")
	}
	sent := at(7)
	s.lastDigestAt = &sent
	if digestDue(s, at(9)) {
		t.Error("sent at 07:30 today: not due at 09:30")
	}
	yesterday := at(7).Add(-24 * time.Hour)
	s.lastDigestAt = &yesterday
	if digestDue(s, at(6)) {
		t.Error("sent yesterday at 07:30: not due before 07:00 today")
	}
	if !digestDue(s, at(7)) {
		t.Error("sent yesterday: due after 07:00 today")
	}
}

func TestRetryDelayGrows(t *testing.T) {
	prev := time.Duration(0)
	for a := 1; a <= 6; a++ {
		d := retryDelay(a)
		if d < prev {
			t.Fatalf("attempt %d waits %v, less than %v", a, d, prev)
		}
		prev = d
	}
	if retryDelay(1) != time.Minute || retryDelay(10) != 4*time.Hour {
		t.Fatalf("unexpected delays %v %v", retryDelay(1), retryDelay(10))
	}
}

func TestGroupMessage(t *testing.T) {
	s := notifySettings()
	s.ConsoleURL = "https://central.example.test"
	one := []outboxRow{{kind: "appliance_offline", delivery: DeliveryImmediate, subject: "Appliance SN1 is offline", body: "body"}}
	subj, body := groupMessage(one, s)
	if subj != "Appliance SN1 is offline" || !strings.Contains(body, "https://central.example.test/system/notifications") {
		t.Errorf("single: %q %q", subj, body)
	}
	two := append(one, outboxRow{kind: "licence_expiring", delivery: DeliveryImmediate, subject: "Licence", body: "b2"})
	subj, body = groupMessage(two, s)
	if subj != "OneGate Central: 2 new notices" || !strings.Contains(body, "1. Licence") || !strings.Contains(body, "2. Appliance SN1") {
		t.Errorf("grouped: %q %q", subj, body)
	}
	digest := []outboxRow{{kind: "appliance_waiting", delivery: DeliveryDigest, subject: "Waiting", body: "b"}}
	if subj, _ = groupMessage(digest, s); subj != "OneGate Central daily digest: 1 notice" {
		t.Errorf("digest: %q", subj)
	}
}

func TestValidConsoleURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"":                                   true,
		"https://central.example.test":       true,
		"https://central.example.test/admin": true,
		"http://central.example.test":        false,
		"https://user@central.example.test":  false,
		"https://central.example.test/?a=b":  false,
		"javascript:alert(1)":                false,
		"https://central.example.test/#frag": false,
	} {
		if got := validConsoleURL(raw); got != want {
			t.Errorf("%q: %v, want %v", raw, got, want)
		}
	}
}
