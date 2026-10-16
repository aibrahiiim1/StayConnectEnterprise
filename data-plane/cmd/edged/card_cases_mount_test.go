package main

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// CARD DISPUTES ARE REACHABLE WHATEVER THE REVIEW FLAG (audit O-23). With room-charge review off, edged mounts
// the card-case routes alone under the financial-review key, and nothing else of the review surface.
func TestCardCasesAreMountedAloneWhenReviewIsOff(t *testing.T) {
	s := &server{}
	var got []string
	if err := chi.Walk(s.cardCasesRoutes().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		got = append(got, method+" "+route)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	want := []string{"GET /card-cases", "POST /card-cases/{id}/resolve"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("routes = %v, want %v", got, want)
	}
	src := readSourceFile(t, "main.go")
	if !strings.Contains(src, `mountResource(r, s, "financial-review", s.cardCasesRoutes)`) {
		t.Fatal("main.go no longer mounts the card cases when review is off")
	}
}
