package nft

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
	"testing"
	"time"
)

// batchKernel is a small stateful nft: three sets, `-j list set`, `list set` (key type probe) and `-f -` scripts applied as one transaction.
type batchKernel struct {
	sets    map[string]map[string]int // set -> normalised key -> timeout seconds
	execs   []string
	scripts []string
	// reject fails a whole script that contains the text; beforeScript runs once before the first script (to model an element lapsing).
	reject       string
	beforeScript func()
	ignoreDelete string // a delete of a key containing this text is accepted and does nothing
}

func newBatchKernel() *batchKernel {
	return &batchKernel{sets: map[string]map[string]int{Phase3AuthV4: {}, Phase3MACV4: {}}}
}

func (k *batchKernel) client() *Client {
	return NewWithExec(k.exec, k.execIn)
}

func (k *batchKernel) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	k.execs = append(k.execs, strings.Join(args, " "))
	switch {
	case len(args) == 5 && args[0] == "list":
		return []byte("type ifname . ipv4_addr"), nil
	case len(args) == 6 && args[0] == "-j":
		keys := make([]string, 0)
		for key := range k.sets[args[5]] {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var elems []any
		for _, key := range keys {
			elems = append(elems, map[string]any{"elem": map[string]any{"val": map[string]any{"concat": splitConcat(key)}, "timeout": k.sets[args[5]][key], "expires": k.sets[args[5]][key]}})
		}
		out, _ := json.Marshal(map[string]any{"nftables": []any{map[string]any{"set": map[string]any{"elem": elems}}}})
		return out, nil
	}
	return k.apply(strings.Join(args, " "))
}

func (k *batchKernel) execIn(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	k.execs = append(k.execs, strings.Join(args, " "))
	k.scripts = append(k.scripts, string(stdin))
	if k.beforeScript != nil {
		f := k.beforeScript
		k.beforeScript = nil
		f()
	}
	return k.apply(string(stdin))
}

func splitConcat(key string) []any {
	var parts []any
	for _, p := range strings.Split(key, "|") {
		parts = append(parts, p)
	}
	return parts
}

func (k *batchKernel) apply(script string) ([]byte, error) {
	if k.reject != "" && strings.Contains(script, k.reject) {
		return []byte("Error: rejected"), fmt.Errorf("exit status 1")
	}
	staged := map[string]map[string]int{}
	for s, m := range k.sets {
		c := map[string]int{}
		for kk, v := range m {
			c[kk] = v
		}
		staged[s] = c
	}
	for _, cmd := range strings.FieldsFunc(strings.ReplaceAll(script, "\n", ";"), func(r rune) bool { return r == ';' }) {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			continue
		}
		f := strings.Fields(cmd)
		set := f[4]
		body := cmd[strings.Index(cmd, "{")+1 : strings.LastIndex(cmd, "}")]
		secs := 0
		if i := strings.Index(body, " timeout "); i >= 0 {
			fmt.Sscanf(strings.TrimSpace(body[i+len(" timeout "):]), "%ds", &secs)
			body = body[:i]
		}
		key := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(body), `"`, ""), " . ", "|"), " ", "")
		if f[0] == "add" {
			if _, ok := staged[set][key]; !ok {
				staged[set][key] = secs
			}
			continue
		}
		if k.ignoreDelete != "" && strings.Contains(key, k.ignoreDelete) {
			continue
		}
		if _, ok := staged[set][key]; !ok {
			return []byte("Error: No such file or directory"), fmt.Errorf("exit status 1")
		}
		delete(staged[set], key)
	}
	k.sets = staged
	return nil, nil
}

func (k *batchKernel) count(prefix string) int {
	n := 0
	for _, e := range k.execs {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}

func ip(s string) net.IP { return net.ParseIP(s) }

const tmac = "02:00:00:00:00:01"

func TestLeaseBatchIsOneTransactionWithOneReadBeforeAndAfter(t *testing.T) {
	k := newBatchKernel()
	k.sets[Phase3AuthV4]["br-g|10.0.0.1"] = 30 // present: refreshed by delete+add
	c := k.client()
	reqs := []LeaseReq{
		{Iface: "br-g", IP: ip("10.0.0.1"), MAC: tmac, TTL: 90 * time.Second},
		{Iface: "br-g", IP: ip("10.0.0.2"), MAC: "02:00:00:00:00:02", TTL: 90 * time.Second},
	}
	for i, err := range c.LeaseBatch(context.Background(), reqs, false) {
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if len(k.scripts) != 1 {
		t.Fatalf("scripts = %d, want one transaction", len(k.scripts))
	}
	s := k.scripts[0]
	if strings.Count(s, "delete element inet stayconnect phase3_auth_ipv4") != 1 || strings.Count(s, "add element inet stayconnect phase3_auth_ipv4") != 2 {
		t.Fatalf("script must delete+add the present element and add the absent one:\n%s", s)
	}
	// 1 probe + (ip + mac) read before + (ip) read after
	if lists := k.count("list set") + k.count("-j list set"); lists > 5 {
		t.Fatalf("a batch of two used %d reads", lists)
	}
	if got := k.sets[Phase3AuthV4]["br-g|10.0.0.1"]; got != 90 {
		t.Fatalf("lease of the refreshed element = %ds, want exactly the requested 90", got)
	}
	if _, ok := k.sets[Phase3MACV4]["br-g|10.0.0.2|"+"02:00:00:00:00:02"]; !ok {
		t.Fatalf("MAC-bound element missing: %v", k.sets[Phase3MACV4])
	}
}

func TestLeaseBatchCostDoesNotGrowWithTheBatch(t *testing.T) {
	cost := func(n int) int {
		k := newBatchKernel()
		c := k.client()
		var reqs []LeaseReq
		for i := 0; i < n; i++ {
			reqs = append(reqs, LeaseReq{Iface: "br-g", IP: ip(fmt.Sprintf("10.0.%d.%d", i/200, 1+i%200)), TTL: 90 * time.Second})
		}
		for _, err := range c.LeaseBatch(context.Background(), reqs, false) {
			if err != nil {
				t.Fatal(err)
			}
		}
		return len(k.execs)
	}
	if a, b := cost(10), cost(200); a != b {
		t.Fatalf("process count grew with the batch: %d for 10, %d for 200", a, b)
	}
}

func TestLeaseBatchRetriesWhenAnElementLapsedAfterTheRead(t *testing.T) {
	k := newBatchKernel()
	k.sets[Phase3AuthV4]["br-g|10.0.0.1"] = 5
	k.beforeScript = func() { delete(k.sets[Phase3AuthV4], "br-g|10.0.0.1") } // lapses between the read and the write
	c := k.client()
	errs := c.LeaseBatch(context.Background(), []LeaseReq{{Iface: "br-g", IP: ip("10.0.0.1"), TTL: 90 * time.Second}}, false)
	if errs[0] != nil {
		t.Fatalf("a lapse between read and write must be retried on a fresh read, got %v", errs[0])
	}
	if len(k.scripts) != 2 {
		t.Fatalf("scripts = %d, want the refused one and its retry", len(k.scripts))
	}
	if k.sets[Phase3AuthV4]["br-g|10.0.0.1"] != 90 {
		t.Fatal("element not re-added")
	}
}

func TestLeaseBatchAttributesAnUnwritableElementToItself(t *testing.T) {
	k := newBatchKernel()
	k.reject = "10.0.0.3"
	c := k.client()
	var reqs []LeaseReq
	for _, a := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4"} {
		reqs = append(reqs, LeaseReq{Iface: "br-g", IP: ip(a), TTL: 90 * time.Second})
	}
	errs := c.LeaseBatch(context.Background(), reqs, false)
	for i, err := range errs {
		if (i == 2) != (err != nil) {
			t.Fatalf("request %d: err=%v; only the third (10.0.0.3) may fail", i, err)
		}
	}
	for _, a := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.4"} {
		if _, ok := k.sets[Phase3AuthV4]["br-g|"+a]; !ok {
			t.Fatalf("%s must be authorized although its neighbour in the batch could not be", a)
		}
	}
	if _, ok := k.sets[Phase3AuthV4]["br-g|10.0.0.3"]; ok {
		t.Fatal("the unwritable element appeared")
	}
}

func TestLeaseBatchRefusesUnboundedAndSubSecondLeases(t *testing.T) {
	k := newBatchKernel()
	c := k.client()
	errs := c.LeaseBatch(context.Background(), []LeaseReq{
		{Iface: "br-g", IP: ip("10.0.0.1"), TTL: 0},
		{Iface: "br-g", IP: ip("10.0.0.2"), TTL: 200 * time.Millisecond},
		{Iface: "br-g", IP: ip("10.0.0.3"), TTL: 10 * time.Second},
	}, false)
	if errs[0] != ErrUnboundedLease || errs[1] != ErrLeaseTooShort || errs[2] != nil {
		t.Fatalf("errs = %v", errs)
	}
	if len(k.sets[Phase3AuthV4]) != 1 {
		t.Fatalf("only the valid lease may be written: %v", k.sets[Phase3AuthV4])
	}
}

func TestLeaseBatchReportsAnElementThatIsNotThereAfterTheWrite(t *testing.T) {
	// A write that returned success but left no element would look like a healthy guest: the verification read must catch it.
	k := newBatchKernel()
	c := k.client()
	orig := k.exec
	_ = orig
	var dropped bool
	c.execIn = func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
		out, err := k.execIn(ctx, stdin, name, args...)
		if !dropped {
			dropped = true
			delete(k.sets[Phase3AuthV4], "br-g|10.0.0.1")
		}
		return out, err
	}
	errs := c.LeaseBatch(context.Background(), []LeaseReq{{Iface: "br-g", IP: ip("10.0.0.1"), TTL: 90 * time.Second}}, false)
	if errs[0] == nil {
		t.Fatal("an authorization absent after the write must be reported")
	}
}

func TestRevokeBatchRemovesBothElementsAndProvesIt(t *testing.T) {
	k := newBatchKernel()
	k.sets[Phase3AuthV4]["br-g|10.0.0.1"] = 60
	k.sets[Phase3MACV4]["br-g|10.0.0.1|"+tmac] = 60
	k.sets[Phase3AuthV4]["br-g|10.0.0.2"] = 60 // not asked for: must survive
	c := k.client()
	errs := c.RevokeBatch(context.Background(), []AddrReq{{Iface: "br-g", IP: ip("10.0.0.1")}, {Iface: "br-g", IP: ip("10.0.0.9")}}, true)
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errs = %v (an absent address is removed, not an error)", errs)
	}
	if len(k.sets[Phase3MACV4]) != 0 || len(k.sets[Phase3AuthV4]) != 1 {
		t.Fatalf("state after revoke: %v", k.sets)
	}
	if len(k.scripts) != 1 {
		t.Fatalf("scripts = %d", len(k.scripts))
	}
}

func TestRevokeBatchOfAbsentAddressesWritesNothing(t *testing.T) {
	k := newBatchKernel()
	c := k.client()
	if errs := c.RevokeBatch(context.Background(), []AddrReq{{Iface: "br-g", IP: ip("10.0.0.9")}}, true); errs[0] != nil {
		t.Fatal(errs[0])
	}
	if len(k.scripts) != 0 {
		t.Fatal("nothing present, nothing to write")
	}
}

func TestRevokeBatchReportsWhatItCouldNotRemove(t *testing.T) {
	k := newBatchKernel()
	k.sets[Phase3AuthV4]["br-g|10.0.0.1"] = 60
	k.sets[Phase3AuthV4]["br-g|10.0.0.2"] = 60
	k.ignoreDelete = "10.0.0.2" // the kernel "accepts" the delete and keeps the element
	c := k.client()
	errs := c.RevokeBatch(context.Background(), []AddrReq{{Iface: "br-g", IP: ip("10.0.0.1")}, {Iface: "br-g", IP: ip("10.0.0.2")}}, false)
	if errs[0] != nil {
		t.Fatalf("10.0.0.1 was removed: %v", errs[0])
	}
	if errs[1] != ErrStillPresent {
		t.Fatalf("10.0.0.2 is still authorized and must say so, got %v", errs[1])
	}
}

func TestRevokeBatchAttributesAnUndeletableElement(t *testing.T) {
	k := newBatchKernel()
	for _, a := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"} {
		k.sets[Phase3AuthV4]["br-g|"+a] = 60
	}
	k.reject = "10.0.0.2"
	c := k.client()
	var reqs []AddrReq
	for _, a := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"} {
		reqs = append(reqs, AddrReq{Iface: "br-g", IP: ip(a)})
	}
	errs := c.RevokeBatch(context.Background(), reqs, false)
	if errs[0] != nil || errs[2] != nil || errs[1] == nil {
		t.Fatalf("errs = %v; only 10.0.0.2 may fail", errs)
	}
	if _, ok := k.sets[Phase3AuthV4]["br-g|10.0.0.1"]; ok {
		t.Fatal("10.0.0.1 must still be removed")
	}
	if _, ok := k.sets[Phase3AuthV4]["br-g|10.0.0.2"]; !ok {
		t.Fatal("10.0.0.2 is the one that stays, and it is reported")
	}
}
