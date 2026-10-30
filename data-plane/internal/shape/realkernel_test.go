package shape

// Real-kernel proof of the classifier at scale (SHAPE_REAL=1, root, a privileged container with tc, ip and python3):
//
//	SHAPE_REAL=1 SHAPE_REAL_N=8000 go test ./internal/shape -run TestRealKernel -v
//
// It builds a bridge with a /17 address, a network namespace behind a veth port standing for a guest host, installs N
// sessions through the real Client, and measures with real TCP traffic: the rate each sampled session gets in each
// direction, that the class counters carry exactly that traffic, that no unsampled class carries any, the cost of admitting
// 300 more sessions on top, and the kernel memory the classes take.

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

const rkPy = `
import socket, sys, time
mode = sys.argv[1]
if mode == "recv":
    bind, port, secs = sys.argv[2], int(sys.argv[3]), float(sys.argv[4])
    s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind((bind, port)); s.listen(1); s.settimeout(secs + 10)
    c, _ = s.accept(); n = 0; t0 = time.time()
    while True:
        d = c.recv(1 << 16)
        if not d: break
        n += len(d)
    print(n, time.time() - t0)
else:
    src, dst, port, secs = sys.argv[2], sys.argv[3], int(sys.argv[4]), float(sys.argv[5])
    s = socket.socket(); s.bind((src, 0))
    for _ in range(50):
        try: s.connect((dst, port)); break
        except OSError: time.sleep(0.1)
    buf = b"x" * 65536; end = time.time() + secs
    while time.time() < end: s.sendall(buf)
    s.close()
`

func rkSh(t *testing.T, cmd string) string {
	t.Helper()
	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v: %s", cmd, err, out)
	}
	return string(out)
}

func rkIP(idx int) net.IP { // host index in 10.60.0.0/17
	return net.IPv4(10, 60, byte(idx>>8), byte(idx&255)).To4()
}

func rkSlab() int {
	b, _ := os.ReadFile("/proc/meminfo")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "SUnreclaim:") {
			f := strings.Fields(l)
			v, _ := strconv.Atoi(f[1])
			return v
		}
	}
	return 0
}

func rkMeasure(t *testing.T, idx int, down bool, secs int) float64 {
	t.Helper()
	ip := rkIP(idx).String()
	port := strconv.Itoa(20000 + idx%20000)
	var srv, cli *exec.Cmd
	if down { // host -> guest
		srv = exec.Command("ip", "netns", "exec", "gst", "python3", "/tmp/rk.py", "recv", ip, port, strconv.Itoa(secs))
		cli = exec.Command("python3", "/tmp/rk.py", "send", "10.60.0.1", ip, port, strconv.Itoa(secs))
	} else { // guest -> host
		srv = exec.Command("python3", "/tmp/rk.py", "recv", "10.60.0.1", port, strconv.Itoa(secs))
		cli = exec.Command("ip", "netns", "exec", "gst", "python3", "/tmp/rk.py", "send", ip, "10.60.0.1", port, strconv.Itoa(secs))
	}
	var sout strings.Builder
	srv.Stdout = &sout
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if out, err := cli.CombinedOutput(); err != nil {
		t.Fatalf("client: %v %s", err, out)
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
	return n * 8 / d / 1000 // kbit/s
}

func TestRealKernelClassifierAtScale(t *testing.T) {
	if os.Getenv("SHAPE_REAL") != "1" {
		t.Skip("needs a real kernel: SHAPE_REAL=1 in a privileged container with tc, ip, python3")
	}
	n := 4000
	if v, err := strconv.Atoi(os.Getenv("SHAPE_REAL_N")); err == nil && v > 0 {
		n = v
	}
	const br = "br-st"
	ifb := IFBName(br)
	rkSh(t, "ip netns del gst 2>/dev/null; ip link del "+br+" 2>/dev/null; ip link del "+ifb+" 2>/dev/null; true")
	if err := os.WriteFile("/tmp/rk.py", []byte(rkPy), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command("sh", "-c", "ip netns del gst; ip link del "+br+"; ip link del "+ifb).Run()
	})
	rkSh(t, "ip link add "+br+" type bridge && ip link set "+br+" up && ip addr add 10.60.0.1/17 dev "+br)
	rkSh(t, "ip netns add gst && ip link add vst0 type veth peer name vst1 && ip link set vst1 netns gst && ip link set vst0 master "+br+" && ip link set vst0 up")
	rkSh(t, "ip netns exec gst sh -c 'ip link set lo up; ip link set vst1 up'")

	c := New()
	ctx := context.Background()

	// sampled sessions: both bands, first/last of the original band, the very last
	rate := func(idx int) int { return 1000 * (1 + idx%4) } // 1..4 Mbit
	samples := []int{10, 2000, 4095, 4096, n / 2, n + 1}
	sampleSet := map[int]bool{}
	for _, s := range samples {
		if s > n+1 {
			continue
		}
		sampleSet[s] = true
		rkSh(t, fmt.Sprintf("ip netns exec gst ip addr add %s/17 dev vst1", rkIP(s)))
	}

	slab0 := rkSlab()
	admit := func(from, to int) time.Duration {
		t0 := time.Now()
		for i := from; i < to; i += BatchChunk {
			var items []PrepItem
			var ips []net.IP
			for j := i; j < i+BatchChunk && j < to; j++ {
				r := 5000
				up := 2000
				if sampleSet[j] {
					r, up = rate(j), rate(j)
				}
				items = append(items, PrepItem{IP: rkIP(j), DownKbps: r, UpKbps: up})
				ips = append(ips, rkIP(j))
			}
			if err := c.PrepareBatch(ctx, br, items); err != nil {
				t.Fatalf("prepare chunk at %d: %v", i, err)
			}
			if err := c.ActivateBatch(ctx, br, ips); err != nil {
				t.Fatalf("activate chunk at %d: %v", i, err)
			}
		}
		return time.Since(t0)
	}
	d := admit(2, n+2)
	t.Logf("REAL admit %d sessions on one bridge: %.1fs (%.1f ms/session), kernel memory +%d MB", n, d.Seconds(), d.Seconds()*1000/float64(n), (rkSlab()-slab0)/1024)

	// every session forwards in both directions, from one read per device
	var ips []net.IP
	for i := 2; i < n+2; i++ {
		ips = append(ips, rkIP(i))
	}
	fw, err := c.ForwardingBatch(ctx, br, ips)
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range ips {
		if !fw[ip.String()] {
			t.Fatalf("session %s is not forwarding in both directions", ip)
		}
	}

	// per-session rate enforcement, real traffic
	before := map[string][]map[int]ClassBytes{}
	rd := func() []map[int]ClassBytes {
		a, e1 := c.ReadClasses(ctx, br)
		b, e2 := c.ReadClasses(ctx, ifb)
		if e1 != nil || e2 != nil {
			t.Fatal(e1, e2)
		}
		return []map[int]ClassBytes{a, b}
	}
	before["x"] = rd()
	type meas struct {
		idx        int
		down, up   float64
		want       int
		minor      int
		dBytes, uB uint64
	}
	var ms []meas
	for _, s := range samples {
		if !sampleSet[s] {
			continue
		}
		m, _ := MinorFor(br, rkIP(s))
		pre := rd()
		dn := rkMeasure(t, s, true, 4)
		upr := rkMeasure(t, s, false, 4)
		post := rd()
		ms = append(ms, meas{s, dn, upr, rate(s), m, post[0][m].Bytes - pre[0][m].Bytes, post[1][m].Bytes - pre[1][m].Bytes})
	}
	for _, m := range ms {
		t.Logf("REAL session host-index=%d minor=0x%x configured=%d kbit/s: measured download=%.0f upload=%.0f kbit/s; class bytes down=%d up=%d",
			m.idx, m.minor, m.want, m.down, m.up, m.dBytes, m.uB)
		for dir, got := range map[string]float64{"download": m.down, "upload": m.up} {
			if got < float64(m.want)*0.85 || got > float64(m.want)*1.15 {
				t.Errorf("session %d %s = %.0f kbit/s, want %d +-15%%", m.idx, dir, got, m.want)
			}
		}
		// the class counter is the accounting source: it must have counted the traffic (payload plus headers, so slightly more)
		wantBytes := float64(m.want) * 1000 / 8 * 4
		for dir, b := range map[string]uint64{"download": m.dBytes, "upload": m.uB} {
			if float64(b) < wantBytes*0.8 || float64(b) > wantBytes*1.3 {
				t.Errorf("session %d %s class counter = %d bytes, want about %.0f", m.idx, dir, b, wantBytes)
			}
		}
	}
	// classification stays exact at scale: no unsampled class carried a packet beyond the handshake noise
	post := rd()
	stray := 0
	for _, ip := range ips {
		m, _ := MinorFor(br, ip)
		if sampleSet[int(uint32(ip[2])<<8|uint32(ip[3]))] {
			continue
		}
		if post[0][m].Bytes != before["x"][0][m].Bytes || post[1][m].Bytes != before["x"][1][m].Bytes {
			stray++
		}
	}
	if stray != 0 {
		t.Errorf("%d unsampled sessions' classes counted traffic that was not theirs", stray)
	}

	// 300 more admitted on top of n
	d300 := admit(n+2, n+302)
	t.Logf("REAL admit 300 more on top of %d: %.2fs", n, d300.Seconds())
	if d300 > 8*time.Second {
		t.Errorf("admitting 300 on top of %d took %s", n, d300)
	}

	// teardown: every class and filter goes, proven by a read
	var minors []int
	for i := 2; i < n+302; i++ {
		m, _ := MinorFor(br, rkIP(i))
		minors = append(minors, m)
	}
	t0 := time.Now()
	for i := 0; i < len(minors); i += 256 {
		e := i + 256
		if e > len(minors) {
			e = len(minors)
		}
		if err := c.DeleteClassesBatch(ctx, br, minors[i:e]); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("REAL delete %d sessions: %.1fs", len(minors), time.Since(t0).Seconds())
	left := rd()
	if len(left[0]) != 0 || len(left[1]) != 0 {
		t.Errorf("classes left after teardown: bridge %d, ifb %d", len(left[0]), len(left[1]))
	}
	if out := rkSh(t, "tc filter show dev "+br+" parent 1: | grep -c handle || true"); strings.TrimSpace(out) != "0" {
		t.Errorf("filters left on the bridge: %s", out)
	}
}

// A session classified by the previous netd (a u32 filter at pref = minor) is seen as forwarding, and is released by the same calls as a new one.
func TestRealKernelLegacyU32SessionIsReleased(t *testing.T) {
	if os.Getenv("SHAPE_REAL") != "1" {
		t.Skip("needs a real kernel: SHAPE_REAL=1")
	}
	const br = "br-lg"
	ifb := IFBName(br)
	rkSh(t, "ip link del "+br+" 2>/dev/null; ip link del "+ifb+" 2>/dev/null; true")
	rkSh(t, "ip link add "+br+" type bridge && ip link set "+br+" up && ip addr add 10.61.0.1/20 dev "+br)
	t.Cleanup(func() { _ = exec.Command("sh", "-c", "ip link del "+br+"; ip link del "+ifb).Run() })
	c := New()
	ctx := context.Background()
	ip := net.ParseIP("10.61.0.9").To4()
	if err := c.PrepareSession(ctx, br, ip, 3000, 3000); err != nil {
		t.Fatal(err)
	}
	// install the pre-upgrade filters by hand: one u32 per direction at pref = minor
	m, _ := MinorFor(br, ip)
	cid := classidOf(m)
	rkSh(t, fmt.Sprintf("tc filter add dev %s protocol ip parent 1: pref %d u32 match ip dst %s/32 flowid %s", br, m, ip, cid))
	rkSh(t, fmt.Sprintf("tc filter add dev %s protocol ip parent 1: pref %d u32 match ip src %s/32 flowid %s", ifb, m, ip, cid))
	on, err := c.SessionForwarding(ctx, br, ip)
	if err != nil || !on {
		t.Fatalf("a legacy-classified session must read as forwarding: %v %v", on, err)
	}
	if err := c.RemoveClassification(ctx, br, ip); err != nil {
		t.Fatalf("legacy filters not released: %v", err)
	}
	if on, _ := c.SessionForwarding(ctx, br, ip); on {
		t.Fatal("still forwarding after RemoveClassification")
	}
	// the slot can now be re-activated in the current form, and aborted cleanly
	if err := c.ActivateSession(ctx, br, ip); err != nil {
		t.Fatal(err)
	}
	if on, _ := c.SessionForwarding(ctx, br, ip); !on {
		t.Fatal("not forwarding after activation")
	}
	if err := c.AbortSession(ctx, br, ip); err != nil {
		t.Fatalf("abort: %v", err)
	}
}
