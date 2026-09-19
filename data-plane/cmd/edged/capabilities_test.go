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
	"path/filepath"
	"regexp"
	"sort"
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

// EVERY DESTINATION THE ADMIN OFFERS MUST BE A SURFACE THIS SERVICE CAN REPORT.
//
// /capabilities is only useful if the names in it are the names the navigation asks about. The first live
// sweep after it shipped found the gap the hard way: the License destination named the resource "license",
// which is registered as a plain route rather than through mountResource, so it was never recorded -- and an
// appliance that serves the licence screen perfectly well reported it as not enabled.
//
// This reads the navigation's resource names and checks each one is either mounted through mountResource or
// explicitly recorded. It deliberately does NOT check whether the surface is enabled on any particular
// appliance -- that is the runtime answer /capabilities exists to give. It checks that the name is one edged
// knows how to say at all.
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
