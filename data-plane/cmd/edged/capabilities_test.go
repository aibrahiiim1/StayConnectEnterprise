package main

// WHAT THIS APPLIANCE SERVES, AND WHAT HAPPENS TO THE PARTS IT DOES NOT.
//
// The sweep of the PRE-LIVE Hotel Admin found eight navigation destinations that answered 404 because the
// bundle was built with every capability compiled in and the appliance mounts a subset. These pin the two
// halves of the fix: the appliance reports what it actually mounted, and a disabled feature says so instead
// of being misread as something else.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// readSourceFile reads a file from this package, for the assertions that are about WIRING rather than
// behaviour -- where a surface is registered, and whether the registration records itself.
func readSourceFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func TestCapabilitiesReportsWhatWasMounted(t *testing.T) {
	s := &server{surfaces: newMountedSurfaces()}
	for _, n := range []string{"pms-stays", "sessions", "audit"} {
		s.surfaces.add(n)
	}
	w := httptest.NewRecorder()
	s.capabilities(w, httptest.NewRequest(http.MethodGet, "/capabilities", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var got struct {
		Surfaces []string `json:"surfaces"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	// Sorted, so the answer is stable between restarts and a diff of two appliances is readable.
	want := []string{"audit", "pms-stays", "sessions"}
	if strings.Join(got.Surfaces, ",") != strings.Join(want, ",") {
		t.Errorf("surfaces = %v, want %v", got.Surfaces, want)
	}
}

func TestCapabilitiesOmitsWhatWasNotMounted(t *testing.T) {
	// The point of the endpoint. An appliance that does not mount financial-review must not report it, or the
	// navigation goes on offering a destination that 404s -- which is the defect, restated.
	s := &server{surfaces: newMountedSurfaces()}
	s.surfaces.add("sessions")
	w := httptest.NewRecorder()
	s.capabilities(w, httptest.NewRequest(http.MethodGet, "/capabilities", nil))
	if strings.Contains(w.Body.String(), "financial-review") {
		t.Error("an unmounted surface is being reported as available")
	}
}

// THE SURFACE LIST COMES FROM mountResource ITSELF.
//
// A hand-maintained list would be a second thing to remember, and the one that drifts. This asserts the
// recording happens where a surface actually becomes reachable, so the two cannot disagree.
func TestMountResourceRecordsEverySurfaceItMounts(t *testing.T) {
	src := readSourceFile(t, "main.go")
	i := strings.Index(src, "func mountResource(")
	if i < 0 {
		t.Fatal("mountResource not found")
	}
	body := src[i:]
	if end := strings.Index(body, "\n}"); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "s.surfaces.add(name)") {
		t.Error("mountResource does not record the surface it mounts; /capabilities will drift from reality")
	}
}
