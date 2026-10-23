package main

import (
	"fmt"
	"net"
	"testing"
	"time"
)

func fixedLimiter(at time.Time) *loginLimiter {
	l := newLoginLimiter()
	l.now = func() time.Time { return at }
	l.lastGC = at
	return l
}

func TestOneClientFloodingDoesNotLockOutAnotherClient(t *testing.T) {
	l := fixedLimiter(time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC))
	refused := 0
	for i := 0; i < 1000; i++ {
		if !l.allow(fmt.Sprintf("forgot:x%d@example.org", i), "10.20.0.5", "02:00:00:00:00:05") {
			refused++
		}
	}
	if refused < 1000-loginClientPerMinute {
		t.Fatalf("the flooding client itself was refused only %d times", refused)
	}
	if !l.allow("someone@example.org", "10.20.0.6", "02:00:00:00:00:06") {
		t.Fatal("another client's sign-in was refused because of one client's flood")
	}
}

func TestOneClientCanPutAtMostItsOwnCapIntoTheGlobalCounter(t *testing.T) {
	l := fixedLimiter(time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC))
	for i := 0; i < 500; i++ {
		l.allow(fmt.Sprintf("u%d", i), "10.20.0.5", "02:00:00:00:00:05")
	}
	if got := l.windows["ep"].count; got > loginClientPerMinute {
		t.Fatalf("one client charged %d to the global counter, its cap is %d", got, loginClientPerMinute)
	}
}

func TestTheIdentityCapLandsOnTheClientMakingTheAttempts(t *testing.T) {
	l := fixedLimiter(time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC))
	victim := "victim@example.org"
	for i := 0; i < 15; i++ {
		l.allow(victim, "10.20.0.7", "02:00:00:00:00:07")
	}
	if l.allow(victim, "10.20.0.7", "02:00:00:00:00:07") {
		t.Fatal("the attacking client was not capped on the identity")
	}
	if !l.allow(victim, "10.20.0.8", "02:00:00:00:00:08") {
		t.Fatal("the identity's owner on their own device was refused because someone else typed their address")
	}
}

func TestTheGlobalDamperStillActsOnAnApplianceWideFlood(t *testing.T) {
	l := fixedLimiter(time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC))
	allowed := 0
	for c := 0; c < 100; c++ {
		ip := net.IPv4(10, 21, byte(c/250), byte(c%250+1)).String()
		mac := fmt.Sprintf("02:00:00:01:%02x:%02x", c/256, c%256)
		for i := 0; i < loginClientPerMinute; i++ {
			if l.allow(fmt.Sprintf("u%d-%d", c, i), ip, mac) {
				allowed++
			}
		}
	}
	if allowed != loginGlobalPerMinute {
		t.Fatalf("appliance-wide flood: %d allowed, want the global limit %d", allowed, loginGlobalPerMinute)
	}
}

func TestTheLimiterWindowRollsOver(t *testing.T) {
	at := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	l := fixedLimiter(at)
	for i := 0; i < 50; i++ {
		l.allow("a@example.org", "10.20.0.9", "02:00:00:00:00:09")
	}
	if l.allow("a@example.org", "10.20.0.9", "02:00:00:00:00:09") {
		t.Fatal("not capped")
	}
	l.now = func() time.Time { return at.Add(61 * time.Second) }
	if !l.allow("a@example.org", "10.20.0.9", "02:00:00:00:00:09") {
		t.Fatal("the cap did not end with its window")
	}
}

func TestIdentityPerClientSeparatesClients(t *testing.T) {
	mac1, _ := net.ParseMAC("02:00:00:00:00:01")
	mac2, _ := net.ParseMAC("02:00:00:00:00:02")
	ip := net.ParseIP("10.20.0.1")
	a, b := identityPerClient("x@example.org", ip, mac1), identityPerClient("x@example.org", ip, mac2)
	if a == b {
		t.Fatal("two devices share one identity throttle")
	}
	if identityPerClient("x@example.org", ip, nil) == identityPerClient("x@example.org", net.ParseIP("10.20.0.2"), nil) {
		t.Fatal("two addresses (no device known) share one identity throttle")
	}
	if identityPerClient("x@example.org", ip, mac1) != a {
		t.Fatal("not stable")
	}
}
