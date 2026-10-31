package shape

// Real-kernel proof for a /19 client network (SHAPE_REAL=1, root, a privileged container with tc, ip, python3):
//
//	SHAPE_REAL=1 SHAPE_REAL_N=6000 go test ./internal/shape -run TestRealKernelSlash19 -v
//
// Two bridges, each with a /19 address (10.62.0.1/19 and 10.63.0.1/19), a network namespace behind a veth port per bridge holding the
// sampled guest addresses. N sessions are installed at the HIGH end of bridge A's range (host index 8190 downwards, so the run crosses
// the 4095/4096 band boundary and includes the very last host), a smaller population on bridge B at the same top indexes, and the
// measurements are: admission time, the rate each sampled session gets in each direction with real TCP, that the class counters carry
// exactly that traffic, that no unsampled class and nothing on the other bridge counted a byte, the cost of 300 more on top, that
// teardown leaves no class or filter, and the kernel neighbour table after a sweep of every address of the /19.

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

type rk19Env struct {
	br, ns, v0, v1 string
	octet          int // second octet: 10.<octet>.0.0/19
}

func (e rk19Env) ip(idx int) net.IP {
	return net.IPv4(10, byte(e.octet), byte(idx>>8), byte(idx&255)).To4()
}
func (e rk19Env) gw() string { return fmt.Sprintf("10.%d.0.1", e.octet) }

func (e rk19Env) setup(t *testing.T) {
	t.Helper()
	ifb := IFBName(e.br)
	rkSh(t, fmt.Sprintf("ip netns del %s 2>/dev/null; ip link del %s 2>/dev/null; ip link del %s 2>/dev/null; true", e.ns, e.br, ifb))
	rkSh(t, fmt.Sprintf("ip link add %s type bridge && ip link set %s up && ip addr add %s/19 dev %s", e.br, e.br, e.gw(), e.br))
	rkSh(t, fmt.Sprintf("ip netns add %s && ip link add %s type veth peer name %s && ip link set %s netns %s && ip link set %s master %s && ip link set %s up",
		e.ns, e.v0, e.v1, e.v1, e.ns, e.v0, e.br, e.v0))
	rkSh(t, fmt.Sprintf("ip netns exec %s sh -c 'ip link set lo up; ip link set %s up'", e.ns, e.v1))
	t.Cleanup(func() {
		_ = exec.Command("sh", "-c", fmt.Sprintf("ip netns del %s; ip link del %s; ip link del %s", e.ns, e.br, ifb)).Run()
	})
}

func (e rk19Env) warm(t *testing.T, idx int) {
	t.Helper()
	for i := 0; i < 20; i++ {
		if exec.Command("ping", "-c1", "-W1", e.ip(idx).String()).Run() == nil {
			return
		}
	}
	t.Fatalf("guest address %s never became reachable", e.ip(idx))
}

func (e rk19Env) measure(t *testing.T, idx int, down bool, secs int) (kbps float64, bytes uint64) {
	t.Helper()
	ip := e.ip(idx).String()
	port := strconv.Itoa(20000 + idx%20000)
	var srv, cli *exec.Cmd
	if down { // host -> guest
		srv = exec.Command("ip", "netns", "exec", e.ns, "python3", "/tmp/rk19.py", "recv", ip, port, strconv.Itoa(secs))
		cli = exec.Command("python3", "/tmp/rk19.py", "send", e.gw(), ip, port, strconv.Itoa(secs))
	} else { // guest -> host
		srv = exec.Command("python3", "/tmp/rk19.py", "recv", e.gw(), port, strconv.Itoa(secs))
		cli = exec.Command("ip", "netns", "exec", e.ns, "python3", "/tmp/rk19.py", "send", ip, e.gw(), port, strconv.Itoa(secs))
	}
	var sout, serr strings.Builder
	srv.Stdout, srv.Stderr = &sout, &serr
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if out, err := cli.CombinedOutput(); err != nil {
		t.Fatalf("%s host-index %d down=%v: client: %v %s; server stderr: %s", e.br, idx, down, err, out, serr.String())
	}
	if err := srv.Wait(); err != nil {
		t.Fatalf("server: %v", err)
	}
	f := strings.Fields(sout.String())
	if len(f) < 2 {
		t.Fatalf("server printed %q", sout.String())
	}
	n, _ := strconv.ParseFloat(f[0], 64)
	d, _ := strconv.ParseFloat(f[1], 64)
	return n * 8 / d / 1000, uint64(n)
}

func TestRealKernelSlash19HighEndOfRange(t *testing.T) {
	if os.Getenv("SHAPE_REAL") != "1" {
		t.Skip("needs a real kernel: SHAPE_REAL=1 in a privileged container with tc, ip, python3")
	}
	n := 6000
	if v, err := strconv.Atoi(os.Getenv("SHAPE_REAL_N")); err == nil && v > 0 {
		n = v
	}
	if n > 8000 {
		n = 8000 // a /19 has 8,189 client addresses besides the gateway
	}
	if err := os.WriteFile("/tmp/rk19.py", []byte(rkPy), 0o644); err != nil {
		t.Fatal(err)
	}
	const top = 8190 // 10.x.31.254, the last host of the /19
	A := rk19Env{br: "br-s19a", ns: "gs19a", v0: "vs19a0", v1: "vs19a1", octet: 62}
	B := rk19Env{br: "br-s19b", ns: "gs19b", v0: "vs19b0", v1: "vs19b1", octet: 63}
	A.setup(t)
	B.setup(t)
	ifbA, ifbB := IFBName(A.br), IFBName(B.br)
	c := New()
	ctx := context.Background()

	// The kernel's view of the geometry is what the shaper uses: the /19 on each bridge.
	for _, e := range []rk19Env{A, B} {
		if m, ok := MinorFor(e.br, e.ip(top)); !ok || m != 0x3000+top-4096 {
			t.Fatalf("%s: last host minor %#x ok=%v, want %#x (the bridge's /19 was not read)", e.br, m, ok, 0x3000+top-4096)
		}
	}

	lowest := top - n + 1
	rate := func(idx int) int { return 1000 * (1 + idx%4) }
	samples := []int{top, top - 1, 7000, 4096, 4095, lowest}
	sampleSet := map[int]bool{}
	for _, s := range samples {
		if s < lowest || s > top || sampleSet[s] {
			continue
		}
		sampleSet[s] = true
		rkSh(t, fmt.Sprintf("ip netns exec %s ip addr add %s/19 dev %s", A.ns, A.ip(s), A.v1))
	}
	rkSh(t, fmt.Sprintf("ip netns exec %s ip addr add %s/19 dev %s", B.ns, B.ip(top), B.v1))

	slab0 := rkSlab()
	admit := func(e rk19Env, from, to int) time.Duration { // host indexes (to, from], counting DOWN from `from`
		t0 := time.Now()
		for i := from; i > to; i -= BatchChunk {
			var items []PrepItem
			var ips []net.IP
			for j := i; j > i-BatchChunk && j > to; j-- {
				r, up := 5000, 2000
				if e.br == A.br && sampleSet[j] {
					r, up = rate(j), rate(j)
				}
				items = append(items, PrepItem{IP: e.ip(j), DownKbps: r, UpKbps: up})
				ips = append(ips, e.ip(j))
			}
			if err := c.PrepareBatch(ctx, e.br, items); err != nil {
				t.Fatalf("%s prepare chunk at %d: %v", e.br, i, err)
			}
			if err := c.ActivateBatch(ctx, e.br, ips); err != nil {
				t.Fatalf("%s activate chunk at %d: %v", e.br, i, err)
			}
		}
		return time.Since(t0)
	}
	d := admit(A, top, lowest-1)
	t.Logf("REAL /19 admit %d sessions at the top of one bridge (host index %d..%d): %.1fs (%.1f ms/session), kernel memory +%d MB",
		n, top, lowest, d.Seconds(), d.Seconds()*1000/float64(n), (rkSlab()-slab0)/1024)
	dB := admit(B, top, top-600)
	t.Logf("REAL /19 admit 600 on the second bridge: %.1fs", dB.Seconds())

	// every session forwards in both directions, read from one dump per device
	var ips []net.IP
	for i := top; i >= lowest; i-- {
		ips = append(ips, A.ip(i))
	}
	fw, err := c.ForwardingBatch(ctx, A.br, ips)
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range ips {
		if !fw[ip.String()] {
			t.Fatalf("session %s is not forwarding in both directions", ip)
		}
	}

	rd := func(br string) []map[int]ClassBytes {
		a, e1 := c.ReadClasses(ctx, br)
		b, e2 := c.ReadClasses(ctx, IFBName(br))
		if e1 != nil || e2 != nil {
			t.Fatal(e1, e2)
		}
		return []map[int]ClassBytes{a, b}
	}
	beforeA, beforeB := rd(A.br), rd(B.br)
	type meas struct {
		idx        int
		down, up   float64
		want       int
		minor      int
		dBytes, uB uint64
		appD, appU uint64
	}
	var ms []meas
	for _, s := range samples {
		if !sampleSet[s] {
			continue
		}
		m, _ := MinorFor(A.br, A.ip(s))
		A.warm(t, s)
		pre := rd(A.br)
		dn, dnB := A.measure(t, s, true, 4)
		mid := rd(A.br)
		upr, upB := A.measure(t, s, false, 4)
		post := rd(A.br)
		ms = append(ms, meas{s, dn, upr, rate(s), m, mid[0][m].Bytes - pre[0][m].Bytes, post[1][m].Bytes - mid[1][m].Bytes, dnB, upB})
	}
	for _, m := range ms {
		t.Logf("REAL /19 session host-index=%d (%s) minor=0x%x configured=%d kbit/s: measured download=%.0f upload=%.0f kbit/s; class bytes down=%d up=%d",
			m.idx, A.ip(m.idx), m.minor, m.want, m.down, m.up, m.dBytes, m.uB)
		for dir, got := range map[string]float64{"download": m.down, "upload": m.up} {
			if got < float64(m.want)*0.85 || got > float64(m.want)*1.15 {
				t.Errorf("session %d %s = %.0f kbit/s, want %d +-15%%", m.idx, dir, got, m.want)
			}
		}
		for dir, p := range map[string][2]uint64{"download": {m.dBytes, m.appD}, "upload": {m.uB, m.appU}} {
			if float64(p[0]) < float64(p[1]) || float64(p[0]) > float64(p[1])*1.12 {
				t.Errorf("session %d %s class counter = %d bytes for %d bytes of payload", m.idx, dir, p[0], p[1])
			}
		}
	}
	// no unsampled class on A carried a packet, and nothing at all on bridge B (the same host index, a different device)
	postA, postB := rd(A.br), rd(B.br)
	stray := 0
	for i := top; i >= lowest; i-- {
		if sampleSet[i] {
			continue
		}
		m, _ := MinorFor(A.br, A.ip(i))
		if postA[0][m].Bytes != beforeA[0][m].Bytes || postA[1][m].Bytes != beforeA[1][m].Bytes {
			stray++
		}
	}
	if stray != 0 {
		t.Errorf("%d unsampled sessions' classes on bridge A counted traffic that was not theirs", stray)
	}
	for dev := 0; dev < 2; dev++ {
		for m, cb := range postB[dev] {
			if cb.Bytes != beforeB[dev][m].Bytes {
				t.Errorf("bridge B class %#x counted %d bytes while only bridge A carried traffic", m, cb.Bytes-beforeB[dev][m].Bytes)
			}
		}
	}

	// 300 more admitted on top of n, continuing downwards
	d300 := admit(A, lowest-1, lowest-301)
	t.Logf("REAL /19 admit 300 more on top of %d: %.2fs", n, d300.Seconds())
	if d300 > 8*time.Second {
		t.Errorf("admitting 300 on top of %d took %s", n, d300)
	}

	// teardown: every class and filter on both bridges goes, proven by a read
	for _, e := range []struct {
		env      rk19Env
		from, to int
	}{{A, top, lowest - 300}, {B, top, top - 599}} {
		var minors []int
		for i := e.from; i >= e.to; i-- {
			m, _ := MinorFor(e.env.br, e.env.ip(i))
			minors = append(minors, m)
		}
		t0 := time.Now()
		for i := 0; i < len(minors); i += 256 {
			end := i + 256
			if end > len(minors) {
				end = len(minors)
			}
			if err := c.DeleteClassesBatch(ctx, e.env.br, minors[i:end]); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("REAL /19 delete %d sessions on %s: %.1fs", len(minors), e.env.br, time.Since(t0).Seconds())
	}
	for _, br := range []string{A.br, B.br} {
		left := rd(br)
		if len(left[0]) != 0 || len(left[1]) != 0 {
			t.Errorf("%s: classes left after teardown: bridge %d, ifb %d", br, len(left[0]), len(left[1]))
		}
		if out := rkSh(t, "tc filter show dev "+br+" parent 1: | grep -c handle || true"); strings.TrimSpace(out) != "0" {
			t.Errorf("%s: filters left on the bridge: %s", br, out)
		}
	}
	_, _ = ifbA, ifbB
}

// The neighbour (ARP) table with a sweep of EVERY address of a /19 on one bridge, as a client scanning the network would do (and as a
// bridge full of hosts would populate over a day). The appliance sets net.ipv4.neigh.default.gc_thresh1/2/3 = 8192/32768/65536
// (deploy/sysctl/99-stayconnect.conf); this test applies those values only if the kernel's are lower, then reports the table and any
// overflow the kernel logged.
func TestRealKernelSlash19NeighbourTable(t *testing.T) {
	if os.Getenv("SHAPE_REAL") != "1" {
		t.Skip("needs a real kernel: SHAPE_REAL=1")
	}
	E := rk19Env{br: "br-s19n", ns: "gs19n", v0: "vs19n0", v1: "vs19n1", octet: 64}
	E.setup(t)
	read := func(p string) int {
		b, err := os.ReadFile("/proc/sys/net/ipv4/neigh/default/" + p)
		if err != nil {
			return -1 // not exposed to this container's namespace; the table itself is global to the kernel
		}
		v, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		return v
	}
	for p, want := range map[string]int{"gc_thresh1": 8192, "gc_thresh2": 32768, "gc_thresh3": 65536} {
		if cur := read(p); cur >= 0 && cur < want {
			t.Logf("kernel %s = %d is below the appliance's %d: raising it for this run (as deploy/sysctl/99-stayconnect.conf does)", p, cur, want)
			if err := os.WriteFile("/proc/sys/net/ipv4/neigh/default/"+p, []byte(strconv.Itoa(want)), 0o644); err != nil {
				t.Logf("could not raise %s: %v (the run is then not representative of the appliance)", p, err)
			}
		}
	}
	// a guest port that answers a few addresses; the rest of the sweep stays unresolved (INCOMPLETE then FAILED), the worst case for table size
	for _, i := range []int{2, 4095, 4096, 8190} {
		rkSh(t, fmt.Sprintf("ip netns exec %s ip addr add %s/19 dev %s", E.ns, E.ip(i), E.v1))
	}
	py := `
import resource, socket, sys, time
resource.setrlimit(resource.RLIMIT_NOFILE, (65536, 65536))
keep = []
sent = 0
for i in range(2, 8191):
    # one socket per destination: the datagram waits in the unresolved queue of its own socket's send buffer, so the sweep is not
    # throttled to the first few hundred addresses by one socket's buffer
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_BINDTODEVICE, sys.argv[1].encode())
    s.setblocking(False)
    try:
        s.sendto(b"x", ("10.%s.%d.%d" % (sys.argv[2], i >> 8, i & 255), 9)); sent += 1
    except OSError:
        pass
    keep.append(s)
time.sleep(1)
print(sent)
`
	if err := os.WriteFile("/tmp/rk19sweep.py", []byte(py), 0o644); err != nil {
		t.Fatal(err)
	}
	overflows := func() int {
		o, _ := exec.Command("sh", "-c", "dmesg 2>/dev/null | grep -c 'neighbor table overflow' || true").Output()
		v, _ := strconv.Atoi(strings.TrimSpace(string(o)))
		return v
	}
	ov0 := overflows()
	t0 := time.Now()
	out := rkSh(t, fmt.Sprintf("python3 /tmp/rk19sweep.py %s %d", E.br, E.octet))
	time.Sleep(1 * time.Second)
	count := func() int {
		o := rkSh(t, fmt.Sprintf("ip -4 neigh show dev %s | wc -l", E.br))
		v, _ := strconv.Atoi(strings.TrimSpace(o))
		return v
	}
	n := count()
	t.Logf("REAL /19 neighbour sweep: sent to %s addresses in %.1fs; %d neighbour entries on the bridge; gc_thresh1/2/3 as seen here = %d/%d/%d (-1 = not exposed to the container); NEW 'neighbor table overflow' lines in dmesg: %d",
		strings.TrimSpace(out), time.Since(t0).Seconds(), n, read("gc_thresh1"), read("gc_thresh2"), read("gc_thresh3"), overflows()-ov0)
	if d := overflows() - ov0; d != 0 {
		t.Errorf("the kernel logged %d neighbour table overflows during the sweep: the table is smaller than a /19", d)
	}
	if n < 8000 {
		t.Errorf("only %d of ~8189 sweep addresses have a neighbour entry: the sweep did not populate the table", n)
	}
	if n > 65536 {
		t.Errorf("table (%d) is larger than the hard limit", n)
	}
	// the entries exist and the table is not refusing: a fresh address still resolves
	if err := exec.Command("ping", "-c1", "-W2", E.ip(8190).String()).Run(); err != nil {
		t.Errorf("the last host of the /19 does not answer after the sweep: %v", err)
	}
}
