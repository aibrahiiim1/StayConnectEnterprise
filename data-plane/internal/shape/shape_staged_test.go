package shape

// Command-level proof of staged provisioning without a kernel: a recording runner captures every tc/ip
// call. Prepare adds a class but no forwarding filter; activation adds it; re-rate is `class change`, never
// delete+add; abort removes both and proves the class gone.

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
)

type recordingRunner struct {
	mu   sync.Mutex
	cmds []string
	// canned outputs for the read commands (empty = nothing there)
	classShow  string
	filterShow string
}

func (r *recordingRunner) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	line := name + " " + strings.Join(args, " ")
	r.cmds = append(r.cmds, line)
	switch {
	case strings.Contains(line, "-s class show"):
		return []byte(r.classShow), nil
	case strings.Contains(line, "filter show"):
		return []byte(r.filterShow), nil
	default:
		return nil, nil
	}
}

func (r *recordingRunner) has(substr string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.cmds {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func newTestClient() (*Client, *recordingRunner) {
	rr := &recordingRunner{}
	c := &Client{TCPath: "tc", IPPath: "ip", exec: rr.run,
		infraReady: map[string]bool{}, bridgeMu: map[string]*sync.Mutex{}}
	return c, rr
}

var testIP = net.ParseIP("10.0.0.5")

// A prepared class has the class and leaf qdisc on both bridge and ifb, but no forwarding filter.
func TestPrepareInstallsClassWithoutForwardingFilter(t *testing.T) {
	c, rr := newTestClient()
	if err := c.PrepareSession(context.Background(), "br-guest", testIP, 8000, 3000); err != nil {
		t.Fatal(err)
	}
	cid := ClassidForIP(testIP)
	if !rr.has("class add dev br-guest parent 1: classid " + cid) {
		t.Fatal("no download class was prepared on the bridge")
	}
	if !rr.has("class add dev ifb-guest parent 1: classid " + cid) {
		t.Fatal("no upload class was prepared on the ifb")
	}
	if rr.has("filter add") && rr.has("flowid "+cid) {
		t.Fatal("a guest forwarding filter was installed during PREPARE; the class would carry packets before it is accountable")
	}
}

func TestActivateAddsForwardingFilters(t *testing.T) {
	c, rr := newTestClient()
	if err := c.PrepareSession(context.Background(), "br-guest", testIP, 8000, 3000); err != nil {
		t.Fatal(err)
	}
	if err := c.ActivateSession(context.Background(), "br-guest", testIP); err != nil {
		t.Fatal(err)
	}
	cid := ClassidForIP(testIP)
	if !rr.has("filter add dev br-guest protocol ip parent 1: pref") || !rr.has("match ip dst "+testIP.String()+"/32 flowid "+cid) {
		t.Fatal("the download forwarding filter was not installed on activation")
	}
	if !rr.has("match ip src " + testIP.String() + "/32 flowid " + cid) {
		t.Fatal("the upload forwarding filter was not installed on activation")
	}
}

// A re-rate is `tc class change` (counters preserved), never delete+add (counters reset).
func TestReRateIsClassChangeNotDeleteAdd(t *testing.T) {
	c, rr := newTestClient()
	if err := c.ReRateSession(context.Background(), "br-guest", testIP, 20000, 9000); err != nil {
		t.Fatal(err)
	}
	cid := ClassidForIP(testIP)
	if !rr.has("class change dev br-guest parent 1: classid " + cid) {
		t.Fatal("re-rate did not use `class change` on the bridge")
	}
	if !rr.has("class change dev ifb-guest parent 1: classid " + cid) {
		t.Fatal("re-rate did not use `class change` on the ifb")
	}
	if rr.has("class del dev br-guest") || rr.has("class add dev br-guest parent 1: classid "+cid) {
		t.Fatal("re-rate deleted and re-added the class; the counters would have reset")
	}
}

func TestAbortRemovesAndVerifies(t *testing.T) {
	c, rr := newTestClient()
	rr.classShow = "" // `class show` returns nothing => the class is gone
	if err := c.AbortSession(context.Background(), "br-guest", testIP); err != nil {
		t.Fatalf("abort of an already-empty device should succeed: %v", err)
	}
	if !rr.has("filter del dev br-guest") || !rr.has("filter del dev ifb-guest") {
		t.Fatal("abort did not remove the forwarding filters")
	}
	if !rr.has("class del dev br-guest") || !rr.has("class del dev ifb-guest") {
		t.Fatal("abort did not remove the classes")
	}
}

// Abort errors when it cannot prove the class gone, so the caller can escalate to the quarantine.
func TestAbortFailsWhenClassSurvives(t *testing.T) {
	c, rr := newTestClient()
	minor, _ := MinorForIP(testIP)
	rr.classShow = "class htb 1:" + itoahex(minor) + " parent 1: prio 0 rate 8000Kbit\n Sent 0 bytes 0 pkt\n"
	if err := c.AbortSession(context.Background(), "br-guest", testIP); err == nil {
		t.Fatal("abort reported success while the class was still present")
	}
}

func TestRemoveClassificationStripsFilters(t *testing.T) {
	c, rr := newTestClient()
	if err := c.RemoveClassification(context.Background(), "br-guest", testIP); err != nil {
		t.Fatal(err)
	}
	if !rr.has("filter del dev br-guest") || !rr.has("filter del dev ifb-guest") {
		t.Fatal("classification removal did not remove both filters")
	}
}

func TestSessionForwardingReadsBothDirections(t *testing.T) {
	c, rr := newTestClient()
	minor, _ := MinorForIP(testIP)
	rr.filterShow = "filter parent 1: protocol ip pref " + itoa(minor) + " u32 ..."
	on, err := c.SessionForwarding(context.Background(), "br-guest", testIP)
	if err != nil {
		t.Fatal(err)
	}
	if !on {
		t.Fatal("SessionForwarding did not detect the installed filter on both devices")
	}
}

func itoa(n int) string    { return intToStr(n, 10) }
func itoahex(n int) string { return intToStr(n, 16) }

func intToStr(n, base int) string {
	if n == 0 {
		return "0"
	}
	const digits = "0123456789abcdef"
	var b []byte
	for n > 0 {
		b = append([]byte{digits[n%base]}, b...)
		n /= base
	}
	return string(b)
}
