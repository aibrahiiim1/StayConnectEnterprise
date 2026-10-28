package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"
)

// P5 M1: both listeners come from one constructor carrying every bound.
func TestPortalServersBoundSlowIdleAndOversizedRequests(t *testing.T) {
	s := newPortalServer(":0", http.NotFoundHandler())
	if s.ReadHeaderTimeout != 5*time.Second || s.ReadTimeout != 15*time.Second || s.IdleTimeout != 60*time.Second ||
		s.WriteTimeout <= portalRequestTimeout || s.MaxHeaderBytes != 16<<10 {
		t.Fatalf("server bounds: header %v read %v write %v idle %v maxHeader %d",
			s.ReadHeaderTimeout, s.ReadTimeout, s.WriteTimeout, s.IdleTimeout, s.MaxHeaderBytes)
	}
}

// P5 M1: a body trickled after the headers no longer holds the connection past the read bound.
func TestATrickledBodyIsCutOffByTheReadBound(t *testing.T) {
	readErr := make(chan error, 1)
	s := newPortalServer("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		readErr <- err
	}))
	s.ReadTimeout = 300 * time.Millisecond // the constructor's bound, shortened so the test is quick
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve(ln) }()
	defer s.Close()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = fmt.Fprintf(c, "POST /auth/voucher HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\n")
	stop := make(chan struct{})
	defer close(stop)
	go func() { // one byte every 50 ms: never finishing inside the bound
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				if _, err := c.Write([]byte("a")); err != nil {
					return
				}
			}
		}
	}()
	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("the trickled body was read to the end")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a trickled body must be cut off by the read bound")
	}
}

func catchAllSCD(t *testing.T) (*handler, *auditSCD) {
	return auditHandler(t, map[string]func(map[string]any) (int, string){"/": fixed(200, `{}`)})
}

// P5 M1: one body cap for every guest route, declared or not, before scd is asked.
func TestAnOversizedGuestBodyIsRefusedBeforeScd(t *testing.T) {
	h, f := catchAllSCD(t)
	big := url.Values{"code": {strings.Repeat("A", 1<<20)}}
	rec := serveAcct(h, auditReq(http.MethodPost, "/auth/voucher", big))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a declared 1 MB form: got %d, want 413", rec.Code)
	}
	r := auditReq(http.MethodPost, "/auth/voucher", big)
	r.ContentLength = -1 // sent chunked: no declared length
	_ = serveAcct(h, r)
	r = httptest.NewRequest(http.MethodPost, "/auth/otp/request", strings.NewReader(`{"channel":"sms","destination":"`+strings.Repeat("1", 1<<20)+`"}`))
	r.ContentLength = -1
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "10.20.0.50:4321"
	_ = serveAcct(h, r)
	if n := f.n("/"); n != 0 {
		t.Fatalf("scd was asked %d times for oversized bodies", n)
	}
}

// perf D3-L4: the scd client keeps enough idle connections for a burst.
func TestTheScdClientKeepsIdleConnectionsForABurst(t *testing.T) {
	h, err := newHandler(cfg{ScdSocket: filepath.Join(t.TempDir(), "scd.sock")})
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := h.scd.Transport.(*http.Transport)
	if !ok || tr.MaxIdleConnsPerHost < 32 || tr.IdleConnTimeout != 90*time.Second {
		t.Fatalf("scd transport: %+v", h.scd.Transport)
	}
}

// perf D3-M1: an unknown path is a small redirect to the portal; Apple's probe still gets the page.
func TestUnknownPathsAreASmallRedirectAndTheAppleProbeStillAPage(t *testing.T) {
	h, f := catchAllSCD(t)
	r := auditReq(http.MethodGet, "/v2/analytics/beacon.gif", nil)
	rec := serveAcct(h, r)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != h.probeTarget(r) || rec.Body.Len() > 256 {
		t.Fatalf("catch-all: %d %q (%d bytes)", rec.Code, rec.Header().Get("Location"), rec.Body.Len())
	}
	if f.n("/") != 0 {
		t.Fatal("the catch-all must not ask scd for a design or anything else")
	}
	rec = serveAcct(h, auditReq(http.MethodGet, "/hotspot-detect.html", nil))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "<TITLE>Success</TITLE>") || rec.Body.Len() < 1000 {
		t.Fatalf("Apple's probe must still get the portal page: %d", rec.Code)
	}
}

// P5 L3: expired commerce sessions leave memory.
func TestExpiredCommerceSessionsAreRemoved(t *testing.T) {
	s := newCommerceSessionStore()
	now := time.Now()
	s.now = func() time.Time { return now }
	s.put("t1", commerceSession{expiry: now.Add(10 * time.Minute)})
	now = now.Add(11 * time.Minute)
	if _, ok := s.get("t1"); ok {
		t.Fatal("an expired session must not resolve")
	}
	if _, held := s.m["t1"]; held {
		t.Fatal("an expired session must be removed when it is read")
	}
	for i := 0; i < commerceSessionPruneAt; i++ {
		s.put(fmt.Sprint("old", i), commerceSession{expiry: now.Add(-time.Second)})
	}
	s.put("live", commerceSession{expiry: now.Add(time.Minute)})
	if len(s.m) != 1 {
		t.Fatalf("past the bound, put must prune expired sessions: %d held", len(s.m))
	}
}

// P5 L2: every POST that reaches a scd credential, message, device or commerce route passes the damper.
func TestEverySecretMessageAndDeviceRouteIsDamped(t *testing.T) {
	form := func(path string, v url.Values) *http.Request { return auditReq(http.MethodPost, path, v) }
	js := func(path, body string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.RemoteAddr = "10.20.0.50:4321"
		return r
	}
	formRoutes := map[string]*http.Request{
		"grace":   form("/auth/account/grace", url.Values{"email": {"a@example.com"}, "password": {"pw-long-enough"}}),
		"resend":  form("/auth/account/resend", url.Values{"s": {"sess-ref-123"}}),
		"verify":  form("/account/verify", url.Values{"t": {strings.Repeat("v", 32)}}),
		"reset":   form("/account/reset", url.Values{"t": {strings.Repeat("r", 32)}, "password": {"Long-enough-1"}, "confirm": {"Long-enough-1"}}),
		"acquire": form("/packages/acquire", url.Values{"package_id": {"p"}}),
		"replace": form("/devices/replace", url.Values{"device_id": {"d"}}),
	}
	for name, r := range formRoutes {
		h, f := catchAllSCD(t)
		h.signIns = newSignInDamper()
		for i := 0; i < signInBurst; i++ {
			h.signIns.allow("10.20.0.50")
		}
		r.Header.Set("Accept", "text/html")
		rec := serveAcct(h, r)
		if f.n("/") != 0 || rec.Header().Get("Retry-After") != "60" ||
			!strings.Contains(rec.Body.String(), esc(builtinStrings["en"]["err.attempts"])) {
			t.Errorf("%s: not damped (scd calls %d, Retry-After %q)", name, f.n("/"), rec.Header().Get("Retry-After"))
		}
	}
	budgeted := map[string]struct {
		r    *http.Request
		want string
	}{
		"phase3":         {js("/auth/pms/phase3", `{"room":"101","verification":"Smith","request_id":"11111111-2222-4333-8444-555555555555"}`), guestAuthRateLimitedUnknownMessage},
		"post-stay":      {js("/auth/post-stay-pin", `{"pin":"ABCD2345"}`), guestAuthRateLimitedUnknownMessage},
		"issue":          {js("/poststay/issue", `{}`), guestAuthRateLimitedUnknownMessage},
		"device list":    {js("/devices/list", `{}`), deviceMessage},
		"device release": {js("/devices/release", `{"device_id":"d"}`), deviceMessage},
	}
	for name, c := range budgeted {
		h, f := catchAllSCD(t)
		clock := newRecordingClock()
		h.clock = clock
		h.signIns = newSignInDamper()
		for i := 0; i < signInBurst; i++ {
			h.signIns.allow("10.20.0.50")
		}
		rec := serveAcct(h, c.r)
		var out struct {
			OK      bool   `json:"ok"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if _, waited := clock.waited(); f.n("/") != 0 || rec.Code != http.StatusOK || out.OK || out.Message != c.want || !waited {
			t.Errorf("%s: not damped through its budget (scd calls %d, %d %q, waited %v)", name, f.n("/"), rec.Code, out.Message, waited)
		}
	}
}

// perf D3-L1: the neighbour lookup splits only the line naming the device, and still matches exactly.
func TestTheNeighbourLookupMatchesExactlyWithoutSplittingEveryLine(t *testing.T) {
	var b strings.Builder
	b.WriteString("IP address       HW type     Flags       HW address            Mask     Device\n")
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&b, "10.%d.%d.%d     0x1         0x2         02:00:00:%02x:%02x:%02x     *        br-guest\n", i/65536, (i/256)%256, i%256, i/65536, (i/256)%256, i%256)
	}
	b.WriteString("10.20.0.5        0x1         0x0         00:00:00:00:00:00     *        br-guest\n")
	b.WriteString("10.20.0.50       0x1         0x2         aa:bb:cc:dd:ee:ff     *        br-guest\n")
	table := b.String()
	ip := net.ParseIP("10.20.0.50")
	if mac, ok := arpFind(strings.NewReader(table), ip); !ok || mac.String() != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("lookup: %v %v", mac, ok)
	}
	if _, ok := arpFind(strings.NewReader(table), net.ParseIP("10.20.0.5")); ok {
		t.Fatal("an incomplete (all-zero) entry must not match, nor a longer address sharing its prefix")
	}
	allocs := testing.AllocsPerRun(20, func() { _, _ = arpFind(strings.NewReader(table), ip) })
	if allocs > 50 {
		t.Fatalf("lookup allocates %.0f times for 2,000 neighbours; it must not split every line", allocs)
	}
}

// perf D3-L3 is covered in TestCertificatesAreServedBySNIAndReloadedWhenReplaced.

// perf D3-M2: an expired design is fetched once however many requests arrive together.
func TestADesignExpiryUnderLoadAsksScdOnce(t *testing.T) {
	var calls int32
	release := make(chan struct{})
	h := clientAccessHandler(t, http.NewServeMux())
	h.scd.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		<-release
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"design":{}}`)), Header: http.Header{}}, nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = h.guestDesign(auditReq(http.MethodGet, "/", nil))
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("scd was asked %d times for one design", n)
	}
}

// audit P5 CC4: invisible format characters are written as \u escapes, so a reader sees what a literal holds.
func TestSourcesCarryNoInvisibleFormatCharacters(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, r := range line {
				if unicode.Is(unicode.Cf, r) {
					t.Errorf("%s:%d carries U+%04X; write it as an escape", f, i+1, r)
				}
			}
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// perf D3-L2: uploads (content-addressed names) may be kept by the browser; misses and other names are not.
func TestUploadedImagesAreCacheableAndMissesAreNot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "0123456789abcdef.png"), []byte("\x89PNG\r\n\x1a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logo.png"), []byte("\x89PNG\r\n\x1a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := http.StripPrefix("/assets/", immutableAssets(http.FileServer(neuteredFS{http.Dir(dir)})))
	get := func(p string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		return rec
	}
	if rec := get("/assets/0123456789abcdef.png"); rec.Code != 200 || rec.Header().Get("Cache-Control") != immutableCacheControl {
		t.Fatalf("upload: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if rec := get("/assets/fedcba9876543210.png"); rec.Code != 404 || rec.Header().Get("Cache-Control") != "" {
		t.Fatalf("a miss must not be cacheable: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if rec := get("/assets/logo.png"); rec.Code != 200 || rec.Header().Get("Cache-Control") != "" {
		t.Fatalf("a name that is not content-addressed must not be marked immutable: %q", rec.Header().Get("Cache-Control"))
	}
}
