package pmsloader

// pmsd is the sole owner of a PMS Interface socket. These tests assert the construction and start boundaries
// rather than a database row filter, because the row must not matter.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

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

// The refusal is keyed to the kind, not a switch branch, so re-adding a FIAS case can't bypass it.
func TestBuildByKind_RefusalPrecedesTheSwitch(t *testing.T) {
	for kind := range socketOwningLegacyKinds {
		if _, err := buildByKind(kind, "x"); !errors.Is(err, ErrLegacySocketOwnerRetired) {
			t.Fatalf("kind %q must be refused before any construction branch runs, got %v", kind, err)
		}
	}
}

// Asserts on the socket-owning type, not pms.Starter: Mews and Apaleo implement Starter too and stay started
// (HTTP pollers).
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

func TestStartAll_StillStartsNonSocketProviders(t *testing.T) {
	spy := &pollerSpy{Provider: pms.NewStub("poller")}
	StartAll(context.Background(), []pms.Provider{spy})
	if !spy.started {
		t.Fatal("a non-socket provider was not started; cloud PMS providers would stop refreshing")
	}
}

type pollerSpy struct {
	pms.Provider
	started bool
}

func (s *pollerSpy) Start(context.Context) { s.started = true }
func (s *pollerSpy) Stop()                 {}

// Backstop: a FIAS owner handed in directly, bypassing buildByKind, is still refused.
func TestStartAll_RefusesAFIASSocketOwnerEvenIfHandedOne(t *testing.T) {
	fias := pms.NewProtelFIAS("smuggled-in")
	// The address makes an unrefused Start leave "idle"; without one Start returns early and the test passes vacuously.
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

// The legacy loader must not reference the canonical interface tables or locks pmsd uses; the two ownership
// models must stay independent.
func TestLoader_DoesNotTouchCanonicalInterfaceOwnership(t *testing.T) {
	src := loaderSource(t)
	for _, forbidden := range []string{"pms_interface_runtime", "pms_interface_revisions", "advisory"} {
		if containsFold(src, forbidden) {
			t.Fatalf("the legacy loader references %q; canonical pmsd ownership must stay independent of it", forbidden)
		}
	}
}

// loaderSource reads the source because the property is about what the code does not reference.
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
