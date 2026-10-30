package shape

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
)

type tcRecorder struct {
	execs   []string
	scripts []string
	// failBatch makes any batch whose script contains the text fail the way tc does (non-zero exit, the line number on stderr).
	failBatch string
	filters   map[string]string // device -> `tc filter show` output
	classes   string
}

func (r *tcRecorder) client() *Client {
	return NewWithExec(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		r.execs = append(r.execs, strings.Join(args, " "))
		if len(args) > 1 && args[0] == "filter" && args[1] == "show" {
			return []byte(r.filters[args[3]]), nil
		}
		if len(args) > 1 && args[0] == "-s" {
			return []byte(r.classes), nil
		}
		return nil, nil
	}, func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
		r.execs = append(r.execs, strings.Join(args, " "))
		r.scripts = append(r.scripts, string(stdin))
		if r.failBatch != "" && strings.Contains(string(stdin), r.failBatch) {
			return []byte("RTNETLINK answers: File exists\nCommand failed -:3"), fmt.Errorf("exit status 1")
		}
		return nil, nil
	})
}

func TestPrepareBatchIsOneProcessForAnyNumberOfSessions(t *testing.T) {
	cost := func(n int) (int, string) {
		r := &tcRecorder{}
		c := r.client()
		var items []PrepItem
		for i := 0; i < n; i++ {
			items = append(items, PrepItem{IP: net.ParseIP(fmt.Sprintf("10.20.0.%d", 10+i)), DownKbps: 5000, UpKbps: 2000})
		}
		if err := c.PrepareBatch(context.Background(), "br-g301", items); err != nil {
			t.Fatal(err)
		}
		return len(r.scripts), r.scripts[0]
	}
	if b1, _ := cost(1); b1 != 1 {
		t.Fatalf("batches = %d", b1)
	}
	n, s := cost(30)
	if n != 1 {
		t.Fatalf("batches = %d for 30 sessions", n)
	}
	// each session: class + leaf on the bridge, class + leaf on the ifb; rate == ceil off the root (PER_DEVICE)
	if got := strings.Count(s, "\n"); got != 30*4 {
		t.Fatalf("lines = %d, want 120", got)
	}
	if !strings.Contains(s, "class add dev br-g301 parent 1: classid 1:100a htb rate 5000kbit ceil 5000kbit burst 32k") ||
		!strings.Contains(s, "class add dev ifb-g301 parent 1: classid 1:100a htb rate 2000kbit ceil 2000kbit burst 32k") {
		t.Fatalf("unexpected script:\n%s", s)
	}
}

func TestBatchErrorIsReportedNotSwallowed(t *testing.T) {
	r := &tcRecorder{failBatch: "classid 1:100c"}
	c := r.client()
	err := c.PrepareBatch(context.Background(), "br-g301", []PrepItem{
		{IP: net.ParseIP("10.20.0.10"), DownKbps: 1000, UpKbps: 1000},
		{IP: net.ParseIP("10.20.0.12"), DownKbps: 1000, UpKbps: 1000},
	})
	if err == nil || !strings.Contains(err.Error(), "Command failed") {
		t.Fatalf("a refused batch must surface its error, got %v", err)
	}
}

func TestActivateBatchInstallsBothFiltersPerSession(t *testing.T) {
	r := &tcRecorder{}
	c := r.client()
	if err := c.ActivateBatch(context.Background(), "br-g301", []net.IP{net.ParseIP("10.20.0.10"), net.ParseIP("10.20.0.11")}); err != nil {
		t.Fatal(err)
	}
	s := r.scripts[0]
	if strings.Count(s, "filter add") != 4 || !strings.Contains(s, "dev br-g301 protocol ip parent 1: pref 1 handle 0x100a flower dst_ip 10.20.0.10/32 classid 1:100a") || !strings.Contains(s, "dev ifb-g301 protocol ip parent 1: pref 1 handle 0x100a flower src_ip 10.20.0.10/32 classid 1:100a") {
		t.Fatalf("script:\n%s", s)
	}
}

func TestForwardingBatchReadsEachDeviceOnce(t *testing.T) {
	r := &tcRecorder{filters: map[string]string{
		"br-g301":  "filter protocol ip pref 4106 u32 chain 0 \nfilter protocol ip pref 4107 u32 chain 0 \n",
		"ifb-g301": "filter protocol ip pref 4106 u32 chain 0 \n", // 4107 only forwards one way
	}}
	c := r.client()
	got, err := c.ForwardingBatch(context.Background(), "br-g301", []net.IP{net.ParseIP("10.20.0.10"), net.ParseIP("10.20.0.11"), net.ParseIP("10.20.0.12")})
	if err != nil {
		t.Fatal(err)
	}
	if !got["10.20.0.10"] || got["10.20.0.11"] || got["10.20.0.12"] {
		t.Fatalf("forwarding = %v; only a filter present in BOTH directions counts", got)
	}
	if len(r.execs) != 2 {
		t.Fatalf("execs = %d, want one read per device", len(r.execs))
	}
}

func TestDeleteClassesBatchRemovesFiltersBeforeClasses(t *testing.T) {
	r := &tcRecorder{failBatch: "class del"} // refusals inside the batch are not an error: the caller proves removal with a read
	c := r.client()
	if err := c.DeleteClassesBatch(context.Background(), "br-g301", []int{0x100a, 0x100b}); err != nil {
		t.Fatalf("a refused line must not fail the call: %v", err)
	}
	s := r.scripts[0]
	lastFilter, firstClass := strings.LastIndex(s, "filter del"), strings.Index(s, "class del")
	if lastFilter < 0 || firstClass < 0 || lastFilter > firstClass {
		t.Fatalf("every filter must go before any class, or the class delete is EBUSY and forwarding continues:\n%s", s)
	}
	if err := c.DeleteClassesBatch(context.Background(), "br-g301", []int{0x10}); err == nil {
		t.Fatal("a minor outside the guest range must be refused: it would delete infrastructure")
	}
}

func TestReadClassesReportsInstalledParentAndRates(t *testing.T) {
	r := &tcRecorder{classes: strings.Join([]string{
		"class htb 1:1010 root leaf 1010: prio 0 rate 5Mbit ceil 5Mbit burst 32Kb cburst 1600b ",
		" Sent 120 bytes 3 pkt (dropped 0, overlimits 0 requeues 0) ",
		"",
		"class htb 1:1011 parent 2000: leaf 1011: prio 0 rate 500Kbit ceil 1500Kbit burst 32Kb cburst 1600b ",
		" Sent 7 bytes 1 pkt (dropped 0, overlimits 0 requeues 0) ",
		"",
		"class htb 1:1012 root prio 0 rate 1Gbit ceil 1Gbit burst 32Kb cburst 1600b ",
		"class htb 1:1013 root prio 0 rate 5123Kbit ceil 6Mbit burst 32Kb cburst 1600b ",
		"class htb 1:1014 root prio 0 rate 1.5Mbit ceil 2Mbit burst 32Kb cburst 1600b ",
	}, "\n")}
	got, err := r.client().ReadClasses(context.Background(), "br-g301")
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]ClassBytes{
		0x1010: {Bytes: 120, Packets: 3, Params: true, Parent: "1:", RateKbit: 5000, CeilKbit: 5000},
		0x1011: {Bytes: 7, Packets: 1, Params: true, Parent: "2000:", RateKbit: 500, CeilKbit: 1500},
		0x1012: {Params: true, Parent: "1:", RateKbit: 1000000, CeilKbit: 1000000},
		0x1013: {Params: true, Parent: "1:", RateKbit: 5123, CeilKbit: 6000},
		0x1014: {Params: true, Parent: "1:", RateKbit: 1500, CeilKbit: 2000},
	}
	for m, w := range want {
		if got[m] != w {
			t.Errorf("class %x = %+v, want %+v", m, got[m], w)
		}
	}
}

func TestReadClassesDoesNotGuessFromAHeaderItCannotParse(t *testing.T) {
	r := &tcRecorder{classes: "class htb 1:1010 root prio 0 rate 5.5bogus ceil 5Mbit burst 32Kb\n Sent 1 bytes 1 pkt (dropped 0, overlimits 0 requeues 0) \n"}
	got, _ := r.client().ReadClasses(context.Background(), "br-g301")
	if got[0x1010].Params {
		t.Fatalf("an unparsable rate must leave Params false (the caller then re-rates): %+v", got[0x1010])
	}
	if got[0x1010].Bytes != 1 {
		t.Fatal("counters must still be read")
	}
}
