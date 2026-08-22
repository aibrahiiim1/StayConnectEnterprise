package pmsloader

// THE SINGLE-PMS-OWNER INVARIANT, asserted at the two places it can be broken.
//
// pmsd is the sole owner of a PMS Interface socket. Before this guard, scd could still become a second one:
// pmsloader.Load filtered legacy rows on `enabled = true` and nothing else, buildByKind constructed a FIAS
// client for the three FIAS kinds, and scd's STARTUP passed a process-lifetime context to StartAll. Toggling
// the row looked harmless because all three reload paths pass a context that dies within 30 seconds — it was
// the next scd restart or appliance reboot that produced a permanent competing owner.
//
// These tests are written against the CONSTRUCTION and START boundaries rather than against a live database,
// because that is where the property actually holds. A test that needed Postgres to prove "an enabled row
// cannot start a socket" would be asserting the row filter; the point of the fix is that the row no longer
// matters.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

// An enabled legacy FIAS row cannot produce a provider at all — so there is nothing for scd's startup, or
// any of its reload paths, to start. This is the property that makes a database toggle inert.
func TestBuildByKind_RefusesEverySocketOwningLegacyKind(t *testing.T) {
	for _, kind := range []string{"protel-fias", "opera-fias", "fidelio-fias"} {
		t.Run(kind, func(t *testing.T) {
			prov, err := buildByKind(kind, "legacy-protel")
			if err == nil {
				t.Fatalf("%s was built; an enabled row could then be started by scd", kind)
			}
			if !errors.Is(err, ErrLegacySocketOwnerRetired) {
				t.Fatalf("refusal must be the retired-owner error so the caller can log it as such, got %v", err)
			}
			if prov != nil {
				t.Fatalf("%s returned a provider alongside its error: %T", kind, prov)
			}
		})
	}
}

// The refusal is keyed to the KIND, not to a switch branch. If someone later re-adds a FIAS case to the
// switch, the guard still holds — which is the difference between a fix and a comment asking people not to.
func TestBuildByKind_RefusalPrecedesTheSwitch(t *testing.T) {
	for kind := range socketOwningLegacyKinds {
		if _, err := buildByKind(kind, "x"); !errors.Is(err, ErrLegacySocketOwnerRetired) {
			t.Fatalf("kind %q must be refused before any construction branch runs, got %v", kind, err)
		}
	}
}

// RESTART / REBOOT. scd's startup calls StartAll(rootCtx, built) — a process-lifetime context, and the one
// path that could ever have created a durable socket. A reboot cannot create a competing owner because
// nothing that survives Load is a FIAS socket owner.
//
// The assertion is on the socket-owning TYPE, not on pms.Starter: Mews and Apaleo implement Starter too and
// are deliberately still started, because their loops are HTTP pollers that contend for no socket.
func TestStartAll_NothingBuildableIsAFIASSocketOwner(t *testing.T) {
	for _, kind := range []string{"stub", "mews", "apaleo"} {
		prov, err := buildByKind(kind, "keeper")
		if err != nil {
			t.Fatalf("%s must still build: %v", kind, err)
		}
		if _, isFIAS := prov.(*pms.ProtelFIAS); isFIAS {
			t.Fatalf("%s built a FIAS socket owner; scd startup would open a competing connection", kind)
		}
	}
}

// The retained HTTP-polling providers are still STARTED. Scoping the guard to socket ownership is the whole
// point — a backstop that also disabled Mews and Apaleo would be a regression dressed as a safety fix.
func TestStartAll_StillStartsNonSocketProviders(t *testing.T) {
	spy := &pollerSpy{Provider: pms.NewStub("poller")}
	StartAll(context.Background(), []pms.Provider{spy})
	if !spy.started {
		t.Fatal("a non-socket provider was not started; cloud PMS providers would stop refreshing")
	}
}

// pollerSpy stands in for Mews/Apaleo: it implements Starter but is not a FIAS socket owner.
type pollerSpy struct {
	pms.Provider
	started bool
}

func (s *pollerSpy) Start(context.Context) { s.started = true }
func (s *pollerSpy) Stop()                 {}

// THE BACKSTOP. Handed a real FIAS socket owner directly — bypassing buildByKind entirely, as a future edit
// or a different call site could — scd still refuses to start it. This is what makes the invariant survive a
// change to construction rather than depending on one.
func TestStartAll_RefusesAFIASSocketOwnerEvenIfHandedOne(t *testing.T) {
	fias := pms.NewProtelFIAS("smuggled-in")
	// Configure gives it a real address, so a Start that was NOT refused would enter its run loop and move
	// the status off idle. Without an address Start returns early on its own and the test would pass for the
	// wrong reason — it has to be able to fail.
	if err := fias.Configure(pms.ProviderConfig{
		Name: "smuggled-in", Kind: "protel-fias",
		Connection: pms.ConnectionConfig{Host: "127.0.0.1", Port: 1},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	StartAll(context.Background(), []pms.Provider{fias})
	if got := fias.Health().Status; got != "idle" {
		t.Fatalf("StartAll started a FIAS socket owner (status %q); scd would be a second PMS owner", got)
	}
}

// The retained non-socket providers are unaffected. The guard is scoped to connection ownership, not to
// "legacy" — removing the stub would break development seeding for no safety gain, since it opens nothing.
func TestBuildByKind_KeepsNonSocketProviders(t *testing.T) {
	for _, kind := range []string{"stub", "mews", "apaleo"} {
		prov, err := buildByKind(kind, "keeper")
		if err != nil {
			t.Fatalf("%s is retained and must still build, got %v", kind, err)
		}
		if prov == nil {
			t.Fatalf("%s built nil", kind)
		}
	}
	if _, err := buildByKind("no-such-kind", "x"); err == nil {
		t.Fatal("an unknown kind must still be rejected")
	} else if errors.Is(err, ErrLegacySocketOwnerRetired) {
		t.Fatal("an unknown kind must not be reported as a retired socket owner; the two need different logs")
	}
}

// CANONICAL OWNERSHIP IS UNTOUCHED. This package is scd's legacy loader and has no bearing on pmsd, which
// reads iam_v2.pms_interfaces and holds its own advisory lock. Asserted as an absence: if this package ever
// grows a reference to the canonical interface tables, the two ownership models have started to interact and
// that is exactly the coupling the single-owner rule forbids.
func TestLoader_DoesNotTouchCanonicalInterfaceOwnership(t *testing.T) {
	src := loaderSource(t)
	for _, forbidden := range []string{"pms_interface_runtime", "pms_interface_revisions", "advisory"} {
		if containsFold(src, forbidden) {
			t.Fatalf("the legacy loader references %q; canonical pmsd ownership must stay independent of it", forbidden)
		}
	}
}

// loaderSource reads this package's implementation so a test can assert on what it does NOT reference.
// Reading the source is unusual and deliberate: the property is "these two ownership models do not touch",
// which is a statement about the code rather than about any value it computes at runtime.
func loaderSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("loader.go")
	if err != nil {
		t.Fatalf("read loader.go: %v", err)
	}
	return string(b)
}

func containsFold(hay, needle string) bool {
	return strings.Contains(strings.ToLower(hay), strings.ToLower(needle))
}
