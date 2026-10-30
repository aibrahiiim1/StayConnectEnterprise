package shape

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
)

type clsRunner struct {
	cmds []string
	// addOut / addErr answer a `filter add ... flower` (the probe and a session filter alike).
	flowerOut string
	flowerErr error
	show      map[string]string
}

func (r *clsRunner) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(args, " ")
	r.cmds = append(r.cmds, line)
	if strings.HasPrefix(line, "filter add") && strings.Contains(line, " flower ") && r.flowerErr != nil {
		return []byte(r.flowerOut), r.flowerErr
	}
	if strings.HasPrefix(line, "filter show") {
		return []byte(r.show[args[3]]), nil
	}
	return nil, nil
}

func (r *clsRunner) count(sub string) int {
	n := 0
	for _, c := range r.cmds {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}

func newClsClient(r *clsRunner) *Client { return NewWithExec(r.run, nil) }

func TestProbeSelectsFlowerAndCleansUpAfterItself(t *testing.T) {
	r := &clsRunner{}
	c := newClsClient(r)
	if err := c.ActivateSession(context.Background(), "br-g", net.ParseIP("10.0.0.5")); err != nil {
		t.Fatal(err)
	}
	if r.count("handle 0xfff flower") != 2 { // probe add + probe del
		t.Fatalf("probe should add and remove its filter once: %v", r.cmds)
	}
	if err := c.ActivateSession(context.Background(), "br-g", net.ParseIP("10.0.0.6")); err != nil {
		t.Fatal(err)
	}
	if r.count("handle 0xfff flower") != 2 {
		t.Fatalf("the probe must run once per client: %v", r.cmds)
	}
	if r.count("pref 1 handle 0x1005 flower dst_ip 10.0.0.5/32") != 1 || r.count(" u32 ") != 0 {
		t.Fatalf("expected flower filters only: %v", r.cmds)
	}
}

func TestKernelWithoutFlowerFallsBackToOneU32FilterPerSession(t *testing.T) {
	r := &clsRunner{flowerErr: fmt.Errorf("exit status 2"), flowerOut: "Error: Specified filter kind not found.\nWe have an error talking to the kernel\n"}
	c := newClsClient(r)
	if err := c.ActivateSession(context.Background(), "br-g", net.ParseIP("10.0.0.5")); err != nil {
		t.Fatalf("the fallback must still activate: %v", err)
	}
	if r.count("pref 4101 u32 match ip dst 10.0.0.5/32 flowid 1:1005") != 1 || r.count("pref 4101 u32 match ip src 10.0.0.5/32 flowid 1:1005") != 1 {
		t.Fatalf("expected the original u32 form: %v", r.cmds)
	}
}

func TestInconclusiveProbeDoesNotPinTheClassifier(t *testing.T) {
	r := &clsRunner{flowerErr: fmt.Errorf("exit status 2"), flowerOut: "Cannot find device \"br-g\"\n"}
	c := newClsClient(r)
	_ = c.ActivateSession(context.Background(), "br-g", net.ParseIP("10.0.0.5"))
	r.flowerErr = nil
	_ = c.ActivateSession(context.Background(), "br-g", net.ParseIP("10.0.0.6"))
	if r.count("pref 1 handle 0x1006 flower dst_ip 10.0.0.6/32") != 1 {
		t.Fatalf("a device that was not ready must not pin the fallback: %v", r.cmds)
	}
}

func TestRollbackSwitchForcesU32(t *testing.T) {
	t.Setenv("NETD_SHAPE_CLASSIFIER", "u32")
	r := &clsRunner{}
	c := newClsClient(r)
	_ = c.ActivateSession(context.Background(), "br-g", net.ParseIP("10.0.0.5"))
	if r.count("flower") != 0 || r.count("pref 4101 u32") == 0 {
		t.Fatalf("NETD_SHAPE_CLASSIFIER=u32 must use u32 only: %v", r.cmds)
	}
}

// A filter installed by the previous (u32) netd is released too: both forms are tried, current first.
func TestRemovalReleasesBothFormsOfAFilter(t *testing.T) {
	r := &clsRunner{}
	c := newClsClient(r)
	_ = c.RemoveClassification(context.Background(), "br-g", net.ParseIP("10.0.0.5"))
	// the flower delete "succeeds" in this runner, so the legacy form is not tried
	if r.count("filter del dev br-g protocol ip parent 1: pref 1 handle 0x1005 flower") != 1 {
		t.Fatalf("flower delete missing: %v", r.cmds)
	}
	r2 := &clsRunner{}
	c2 := NewWithExec(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		line := strings.Join(args, " ")
		r2.cmds = append(r2.cmds, line)
		if strings.HasPrefix(line, "filter del") && strings.HasSuffix(line, "flower") {
			return []byte("Error: Specified filter handle not found."), fmt.Errorf("exit status 2")
		}
		return nil, nil
	}, nil)
	if err := c2.RemoveClassification(context.Background(), "br-g", net.ParseIP("10.0.0.5")); err != nil {
		t.Fatalf("a legacy filter is released by the u32 delete: %v", err)
	}
	if r2.count("pref 4101 protocol ip u32") != 2 { // one per device
		t.Fatalf("u32 delete missing: %v", r2.cmds)
	}
}

// Presence reads both forms from one dump per device and nothing else.
func TestForwardingReadsFlowerAndLegacyFilters(t *testing.T) {
	r := &clsRunner{show: map[string]string{
		"br-g": "filter protocol ip pref 1 flower chain 0 \nfilter protocol ip pref 1 flower chain 0 handle 0x1005 classid 1:1005 \n  dst_ip 10.0.0.5\n" +
			"filter protocol ip pref 4102 u32 chain 0 \nfilter protocol ip pref 4102 u32 chain 0 fh 800: ht divisor 1 \n",
		"ifb-g": "filter protocol ip pref 1 flower chain 0 handle 0x1005 classid 1:1005 \nfilter protocol ip pref 4102 u32 chain 0 \n",
	}}
	c := newClsClient(r)
	got, _ := c.ForwardingBatch(context.Background(), "br-g", []net.IP{net.ParseIP("10.0.0.5"), net.ParseIP("10.0.0.6"), net.ParseIP("10.0.0.7")})
	if !got["10.0.0.5"] || !got["10.0.0.6"] || got["10.0.0.7"] {
		t.Fatalf("forwarding = %v", got)
	}
	if len(r.cmds) != 2 {
		t.Fatalf("one dump per device expected: %v", r.cmds)
	}
}

func TestDeleteBatchCoversBothFormsAndTheExtensionBand(t *testing.T) {
	r := &tcRecorder{}
	c := r.client()
	if err := c.DeleteClassesBatch(context.Background(), "br-g301", []int{0x1005, 0x3405}); err != nil {
		t.Fatal(err)
	}
	s := r.scripts[0]
	for _, want := range []string{
		"filter del dev br-g301 protocol ip parent 1: pref 1 handle 0x3405 flower",
		"filter del dev ifb-g301 parent 1: pref 4101 protocol ip u32",
		"class del dev br-g301 classid 1:3405",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if err := c.DeleteClassesBatch(context.Background(), "br-g301", []int{0x2005}); err == nil {
		t.Error("a SHARED group minor must not be deletable as a guest class")
	}
}

func TestReadClassesParsesTheExtensionBand(t *testing.T) {
	r := &tcRecorder{classes: "class htb 1:3405 root leaf 3405: prio 0 rate 5Mbit ceil 5Mbit burst 32Kb cburst 1600b \n Sent 9 bytes 1 pkt (dropped 0, overlimits 0 requeues 0) \n" +
		"class htb 1:2abc root prio 0 rate 5Mbit ceil 5Mbit burst 32Kb\n"}
	got, err := r.client().ReadClasses(context.Background(), "br-g301")
	if err != nil {
		t.Fatal(err)
	}
	if got[0x3405].Bytes != 9 || !got[0x3405].Params {
		t.Fatalf("extension class not read: %+v", got)
	}
	if _, ok := got[0x2abc]; ok {
		t.Fatal("a SHARED group class must not be read as a session class")
	}
}
