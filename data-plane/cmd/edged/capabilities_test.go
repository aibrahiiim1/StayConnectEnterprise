package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// readSourceFile supports assertions about wiring rather than behaviour.
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
	s := &server{surfaces: newMountedSurfaces()}
	s.surfaces.add("sessions")
	w := httptest.NewRecorder()
	s.capabilities(w, httptest.NewRequest(http.MethodGet, "/capabilities", nil))
	if strings.Contains(w.Body.String(), "financial-review") {
		t.Error("an unmounted surface is being reported as available")
	}
}

// Recording must happen in mountResource itself, so a hand-maintained list cannot drift.
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

// Every navigation resource must be mounted through mountResource or explicitly recorded (a plain route such as
// "license" would otherwise be reported as not enabled). It checks the name is one edged can report, not that
// it is enabled on any appliance.
func TestEveryNavigationResourceIsASurfaceEdgedCanReport(t *testing.T) {
	nav, err := os.ReadFile(filepath.Join("..", "..", "..", "hotel-admin", "components", "nav.tsx"))
	if err != nil {
		t.Skipf("the admin is not in this checkout (%v)", err)
	}
	main := readSourceFile(t, "main.go")
	sessions := readSourceFile(t, "resources_sessions.go")
	known := main + sessions

	wanted := map[string]bool{}
	for _, m := range regexp.MustCompile(`resource: "([a-z0-9-]+)"`).FindAllStringSubmatch(string(nav), -1) {
		wanted[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`capability: "([a-z0-9.-]+)"`).FindAllStringSubmatch(string(nav), -1) {
		wanted[m[1]] = true
	}
	if len(wanted) == 0 {
		t.Fatal("no navigation resources were found; this test is not reading what it thinks it is")
	}

	var missing []string
	for name := range wanted {
		mounted := strings.Contains(known, `mountResource(r, s, "`+name+`"`)
		recorded := strings.Contains(known, `s.surfaces.add("`+name+`")`)
		if !mounted && !recorded {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the navigation offers %v, which edged never records as a surface — those destinations "+
			"will report themselves unavailable on every appliance", missing)
	}
}
