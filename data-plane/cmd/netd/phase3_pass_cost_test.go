package main

// Capacity acceptance 2026-10-30: at 300 sign-ins a minute netd's own CPU went to three per-session costs that grow with the number of sessions: two reads
// of /proc for the time and the boot identity, and a scan of the whole DHCP lease list to find one address. Each is now answered from the pass, with the
// same answers.

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTheSecurityClockIsReadOnceNotOncePerSession(t *testing.T) {
	dir := t.TempDir()
	up := filepath.Join(dir, "uptime")
	boot := filepath.Join(dir, "boot_id")
	if err := os.WriteFile(up, []byte("1000.50 4000.00\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(boot, []byte("8f3c1b9a-0d2e-4c55-9d1a-3b6f7e2a1c04\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &procSecurityClock{uptimePath: up, bootIDPath: boot}
	first, err := c.BootMillis()
	if err != nil || first != 1000500 {
		t.Fatalf("first reading %d, %v", first, err)
	}
	id, err := c.BootID()
	if err != nil || id != "8f3c1b9a-0d2e-4c55-9d1a-3b6f7e2a1c04" {
		t.Fatalf("boot id %q, %v", id, err)
	}
	// Remove the files: two thousand more asks in the same instant must not touch them.
	_ = os.Remove(up)
	_ = os.Remove(boot)
	for i := 0; i < 2000; i++ {
		ms, err := c.BootMillis()
		if err != nil {
			t.Fatalf("ask %d re-read the file: %v", i, err)
		}
		if ms < first {
			t.Fatalf("the clock went backwards: %d < %d", ms, first)
		}
		if ms-first > 240 {
			t.Fatalf("a cached reading drifted %d ms", ms-first)
		}
		if got, err := c.BootID(); err != nil || got != id {
			t.Fatalf("ask %d: boot id %q, %v", i, got, err)
		}
	}
	// It does advance with real time between refreshes, and once the refresh period has passed the file is read again and its failure is reported:
	// a clock that cannot be measured is still an error, never a stale number.
	time.Sleep(300 * time.Millisecond)
	if _, err := c.BootMillis(); err == nil {
		t.Fatal("after the refresh period an unreadable clock must be an error")
	}
	time.Sleep(time.Second)
	if _, err := c.BootID(); err == nil {
		t.Fatal("after the refresh period an unreadable boot identity must be an error")
	}
}

func TestACachedClockAdvancesWithTimeBetweenReads(t *testing.T) {
	dir := t.TempDir()
	up := filepath.Join(dir, "uptime")
	if err := os.WriteFile(up, []byte("50.00 1.00\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &procSecurityClock{uptimePath: up, bootIDPath: filepath.Join(dir, "none")}
	a, _ := c.BootMillis()
	time.Sleep(120 * time.Millisecond)
	b, _ := c.BootMillis()
	if d := b - a; d < 100 || d > 200 {
		t.Fatalf("120 ms of real time advanced the cached clock by %d ms", d)
	}
}

type tallyLeases struct {
	rows  []KeaLease
	calls int
}

func (c *tallyLeases) Leases() ([]KeaLease, error) { c.calls++; return c.rows, nil }

// The answer to "whose address is this" must be exactly the one the full scan gives, including the order in which rows of one address are considered
// (the first CURRENT row decides) and the empty and missing cases.
func TestTheIndexedLeaseAnswerEqualsTheFullScan(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cur := func(ip, mac string, state int) KeaLease {
		return KeaLease{IPAddress: ip, HWAddr: mac, State: state, CLTT: now.Unix() - 10, ValidLft: 3600}
	}
	old := func(ip, mac string) KeaLease {
		return KeaLease{IPAddress: ip, HWAddr: mac, State: 0, CLTT: now.Unix() - 7200, ValidLft: 3600}
	}
	rows := []KeaLease{
		cur("10.0.0.2", "aa:aa:aa:aa:aa:01", 0),
		old("10.0.0.3", "aa:aa:aa:aa:aa:02"), // a lapsed row first,
		cur("10.0.0.3", "aa:aa:aa:aa:aa:03", 0), // then the live one
		cur("10.0.0.4", "aa:aa:aa:aa:aa:04", 2), // released: history, not ownership
		cur("10.0.0.5", "aa:aa:aa:aa:aa:05", 0),
		cur("10.0.0.5", "aa:aa:aa:aa:aa:06", 0), // two live rows: the first decides
	}
	src := &tallyLeases{rows: rows}
	scan := addressOwner{src: src, now: func() time.Time { return now }}
	pass := &passLeases{src: src}
	indexed := addressOwner{src: pass, now: func() time.Time { return now }}
	ctx := context.Background()
	cases := []struct {
		ip, mac string
	}{
		{"10.0.0.2", "AA:AA:AA:AA:AA:01"}, {"10.0.0.2", "aa:aa:aa:aa:aa:99"}, {"10.0.0.3", "aa:aa:aa:aa:aa:03"}, {"10.0.0.3", "aa:aa:aa:aa:aa:02"},
		{"10.0.0.4", "aa:aa:aa:aa:aa:04"}, {"10.0.0.5", "aa:aa:aa:aa:aa:05"}, {"10.0.0.5", "aa:aa:aa:aa:aa:06"}, {"10.0.0.9", "aa:aa:aa:aa:aa:07"},
	}
	for _, c := range cases {
		a := scan.Evidence(ctx, net.ParseIP(c.ip), c.mac)
		b := indexed.Evidence(ctx, net.ParseIP(c.ip), c.mac)
		if a.State != b.State || a.Empty != b.Empty || a.Missing != b.Missing || !a.Until.Equal(b.Until) {
			t.Errorf("%s %s: full scan %+v, indexed %+v", c.ip, c.mac, a, b)
		}
	}
	// Eight asks, one read of the lease list.
	src.calls = 0
	pass2 := &passLeases{src: src}
	ix2 := addressOwner{src: pass2, now: func() time.Time { return now }}
	for _, c := range cases {
		_ = ix2.Evidence(ctx, net.ParseIP(c.ip), c.mac)
	}
	if src.calls != 1 {
		t.Fatalf("%d reads of the lease list for %d asks, want 1", src.calls, len(cases))
	}
	// An empty table is still "empty", not "foreign with rows".
	empty := addressOwner{src: &passLeases{src: &tallyLeases{}}, now: func() time.Time { return now }}
	if ev := empty.Evidence(ctx, net.ParseIP("10.0.0.2"), "aa:aa:aa:aa:aa:01"); !ev.Empty || !ev.Missing {
		t.Fatalf("empty table: %+v", ev)
	}
}
