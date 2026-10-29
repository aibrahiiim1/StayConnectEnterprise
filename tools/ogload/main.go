// ogload: a load generator that represents N independent Wi-Fi clients against a TEST OneGate appliance.
//
// Every simulated client is a Linux network namespace holding one macvlan interface with its own MAC address on the
// generator's LAN-side interface, so to the appliance it is a separate device: it takes a real DHCP lease from Kea,
// reaches the real Client Portal (voucher sign-in, the same form a phone posts), and then pulls traffic through the
// appliance's nftables forwarding, shaping, NAT and accounting to a sink server on the WAN side.
//
// Run it only in the isolated lab (the oglab containers); it needs CAP_NET_ADMIN/CAP_SYS_ADMIN and a LAN-side interface.
//
//	ogload setup    -n 2000 -iface eth0 [-parallel 32]            create the namespaces and take DHCP leases -> state file
//	ogload signin   -vouchers file -rate 300 [-count N]            sign clients in through the portal at <rate> per minute
//	ogload traffic  -sink http://IP:8080 -pct 10 -bytes 262144 -every 5s -duration 10m
//	ogload release  -pct 10                                         drop a share of clients (sign-in again later)
//	ogload teardown                                                 remove every namespace
//	ogload sink     -listen :8080                                   the WAN-side server the clients download from
//
// Results are printed as one JSON object per phase on stdout (counts, error classes, latency percentiles).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type Client struct {
	Idx      int    `json:"idx"`
	NS       string `json:"ns"`
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	Gateway  string `json:"gateway"`
	Voucher  string `json:"voucher,omitempty"`
	SignedIn bool   `json:"signed_in"`
}

var stateFile = flag.String("state", "/var/lib/ogload/clients.json", "state file")

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ogload setup|signin|traffic|release|teardown|sink [flags]")
		os.Exit(2)
	}
	cmd := os.Args[1]
	os.Args = append(os.Args[:1], os.Args[2:]...)
	switch cmd {
	case "setup":
		cmdSetup()
	case "signin":
		cmdSignin()
	case "traffic":
		cmdTraffic()
	case "release":
		cmdRelease()
	case "teardown":
		cmdTeardown()
	case "sink":
		cmdSink()
	default:
		fmt.Fprintln(os.Stderr, "unknown command", cmd)
		os.Exit(2)
	}
}

// ---- state

func load() []*Client {
	b, err := os.ReadFile(*stateFile)
	if err != nil {
		fatal("read state: %v", err)
	}
	var cs []*Client
	if err := json.Unmarshal(b, &cs); err != nil {
		fatal("parse state: %v", err)
	}
	return cs
}

func save(cs []*Client) {
	_ = os.MkdirAll(dirOf(*stateFile), 0o755)
	b, _ := json.Marshal(cs)
	if err := os.WriteFile(*stateFile, b, 0o644); err != nil {
		fatal("write state: %v", err)
	}
}

func dirOf(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "."
}

func fatal(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "ogload: "+f+"\n", a...)
	os.Exit(1)
}

func sh(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// ---- setup: namespaces, macvlans, DHCP leases

func cmdSetup() {
	n := flag.Int("n", 100, "clients")
	iface := flag.String("iface", "eth0", "LAN-side interface of this container")
	par := flag.Int("parallel", 24, "concurrent DHCP clients")
	flag.Parse()
	_ = os.MkdirAll("/var/lib/ogload/leases", 0o755)
	cs := make([]*Client, *n)
	var wg sync.WaitGroup
	sem := make(chan struct{}, *par)
	var ok, failed int64
	t0 := time.Now()
	for i := 0; i < *n; i++ {
		c := &Client{Idx: i, NS: fmt.Sprintf("cl%04d", i), MAC: fmt.Sprintf("02:0d:ab:%02x:%02x:%02x", (i>>16)&255, (i>>8)&255, i&255)}
		cs[i] = c
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := provision(c, *iface); err != nil {
				atomic.AddInt64(&failed, 1)
				if atomic.LoadInt64(&failed) <= 5 {
					fmt.Fprintf(os.Stderr, "client %s: %v\n", c.NS, err)
				}
				return
			}
			atomic.AddInt64(&ok, 1)
		}()
	}
	wg.Wait()
	var good []*Client
	for _, c := range cs {
		if c.IP != "" {
			good = append(good, c)
		}
	}
	save(good)
	emit(map[string]any{"phase": "setup", "requested": *n, "leased": ok, "failed": failed, "seconds": time.Since(t0).Seconds()})
}

func provision(c *Client, iface string) error {
	mv := "mv" + c.NS[2:]
	_, _ = sh("ip", "netns", "del", c.NS)
	if o, err := sh("ip", "netns", "add", c.NS); err != nil {
		return fmt.Errorf("netns add: %s", o)
	}
	if o, err := sh("ip", "link", "add", "link", iface, "name", mv, "address", c.MAC, "type", "macvlan", "mode", "bridge"); err != nil {
		return fmt.Errorf("macvlan: %s", o)
	}
	if o, err := sh("ip", "link", "set", mv, "netns", c.NS); err != nil {
		return fmt.Errorf("move: %s", o)
	}
	_, _ = sh("ip", "-n", c.NS, "link", "set", "lo", "up")
	if o, err := sh("ip", "-n", c.NS, "link", "set", mv, "up"); err != nil {
		return fmt.Errorf("up: %s", o)
	}
	lease := "/var/lib/ogload/leases/" + c.NS + ".lease"
	pid := "/var/lib/ogload/leases/" + c.NS + ".pid"
	_ = os.Remove(lease)
	// -1: one attempt; on success dhclient daemonises, so stop it again and keep the address ourselves (2,000 resident clients would waste memory).
	_, _ = sh("ip", "netns", "exec", c.NS, "timeout", "40", "dhclient", "-1", "-lf", lease, "-pf", pid, mv)
	if b, err := os.ReadFile(pid); err == nil {
		if p, e := strconv.Atoi(strings.TrimSpace(string(b))); e == nil {
			_ = syscall.Kill(p, syscall.SIGTERM)
		}
	}
	lb, err := os.ReadFile(lease)
	if err != nil {
		return errors.New("no lease")
	}
	ip, mask, gw := "", "", ""
	for _, ln := range strings.Split(string(lb), "\n") {
		ln = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(ln), ";"))
		switch {
		case strings.HasPrefix(ln, "fixed-address "):
			ip = strings.TrimPrefix(ln, "fixed-address ")
		case strings.HasPrefix(ln, "option subnet-mask "):
			mask = strings.TrimPrefix(ln, "option subnet-mask ")
		case strings.HasPrefix(ln, "option routers "):
			gw = strings.Split(strings.TrimPrefix(ln, "option routers "), ",")[0]
		}
	}
	if ip == "" || mask == "" {
		return errors.New("lease without address")
	}
	m := net.IPMask(net.ParseIP(mask).To4())
	ones, _ := m.Size()
	// dhclient configures the interface on success; make sure the address and route are in place after we stopped it.
	_, _ = sh("ip", "-n", c.NS, "addr", "replace", fmt.Sprintf("%s/%d", ip, ones), "dev", mv)
	if gw != "" {
		_, _ = sh("ip", "-n", c.NS, "route", "replace", "default", "via", gw)
	}
	c.IP, c.Gateway = ip, gw
	return nil
}

func cmdTeardown() {
	flag.Parse()
	cs := load()
	var wg sync.WaitGroup
	sem := make(chan struct{}, 32)
	for _, c := range cs {
		wg.Add(1)
		sem <- struct{}{}
		go func(c *Client) {
			defer wg.Done()
			defer func() { <-sem }()
			_, _ = sh("ip", "netns", "del", c.NS)
		}(c)
	}
	wg.Wait()
	_ = os.Remove(*stateFile)
	emit(map[string]any{"phase": "teardown", "removed": len(cs)})
}

// ---- namespace-bound HTTP

var origNS int

// sysSetns is the setns(2) number; the syscall package does not export it for every architecture.
var sysSetns uintptr = 308

func init() {
	if runtime.GOARCH == "arm64" {
		sysSetns = 268
	}
	fd, err := syscall.Open("/proc/self/ns/net", syscall.O_RDONLY, 0)
	if err == nil {
		origNS = fd
	}
}

// dialIn opens a connection whose socket lives in the client's namespace.
func dialIn(ns string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		fd, err := syscall.Open("/var/run/netns/"+ns, syscall.O_RDONLY, 0)
		if err != nil {
			return nil, err
		}
		defer syscall.Close(fd)
		if _, _, e := syscall.RawSyscall(sysSetns, uintptr(fd), syscall.CLONE_NEWNET, 0); e != 0 {
			return nil, e
		}
		d := net.Dialer{Timeout: 15 * time.Second}
		c, derr := d.DialContext(ctx, network, addr)
		if _, _, e := syscall.RawSyscall(sysSetns, uintptr(origNS), syscall.CLONE_NEWNET, 0); e != 0 {
			if c != nil {
				c.Close()
			}
			return nil, e
		}
		return c, derr
	}
}

func clientHTTP(c *Client, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{DialContext: dialIn(c.NS), MaxIdleConns: 2, IdleConnTimeout: 30 * time.Second, DisableKeepAlives: false},
	}
}

// ---- stats

type stats struct {
	mu  sync.Mutex
	lat []float64
	cls map[string]int
	b   int64
	t0  time.Time
}

func newStats() *stats { return &stats{cls: map[string]int{}, t0: time.Now()} }
func (s *stats) add(cls string, d time.Duration, bytes int64) {
	s.mu.Lock()
	s.cls[cls]++
	s.lat = append(s.lat, float64(d.Milliseconds()))
	s.b += bytes
	s.mu.Unlock()
}
func (s *stats) report(phase string, extra map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sort.Float64s(s.lat)
	p := func(q float64) float64 {
		if len(s.lat) == 0 {
			return 0
		}
		return s.lat[int(q*float64(len(s.lat)-1))]
	}
	m := map[string]any{"phase": phase, "seconds": time.Since(s.t0).Seconds(), "results": s.cls, "n": len(s.lat), "bytes": s.b,
		"p50_ms": p(0.5), "p95_ms": p(0.95), "p99_ms": p(0.99), "max_ms": p(1)}
	for k, v := range extra {
		m[k] = v
	}
	emit(m)
}

func emit(m map[string]any) {
	b, _ := json.Marshal(m)
	fmt.Println(string(b))
}

// ---- signin

func cmdSignin() {
	vf := flag.String("vouchers", "", "file with one voucher code per line")
	rate := flag.Float64("rate", 300, "sign-ins per minute")
	count := flag.Int("count", 0, "stop after this many attempts (0 = every client not yet signed in)")
	portalPort := flag.Int("portal-port", 8380, "portal port on the gateway")
	redo := flag.Bool("redo", false, "also sign in clients already marked signed in (after a release)")
	flag.Parse()
	cs := load()
	var codes []string
	f, err := os.Open(*vf)
	if err != nil {
		fatal("vouchers: %v", err)
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if t := strings.TrimSpace(sc.Text()); t != "" {
			codes = append(codes, t)
		}
	}
	f.Close()
	next := int64(0)
	st := newStats()
	var wg sync.WaitGroup
	interval := time.Duration(float64(time.Minute) / *rate)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	attempts := 0
	for _, c := range cs {
		if c.SignedIn && !*redo {
			continue
		}
		if *count > 0 && attempts >= *count {
			break
		}
		<-tick.C
		i := int(atomic.AddInt64(&next, 1)) - 1
		if i >= len(codes) {
			emit(map[string]any{"warning": "out of vouchers"})
			break
		}
		attempts++
		wg.Add(1)
		go func(c *Client, code string) {
			defer wg.Done()
			hc := clientHTTP(c, 30*time.Second)
			t := time.Now()
			form := url.Values{"code": {code}}
			resp, err := hc.PostForm(fmt.Sprintf("http://%s:%d/auth/voucher", c.Gateway, *portalPort), form)
			d := time.Since(t)
			if err != nil {
				st.add("error:"+short(err), d, 0)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			loc := resp.Header.Get("Location")
			switch {
			case strings.Contains(loc, "/success"):
				c.SignedIn, c.Voucher = true, code
				st.add("ok", d, 0)
			case strings.Contains(loc, "/packages"):
				st.add("needs_package_choice", d, 0)
			default:
				st.add(fmt.Sprintf("refused:%d", resp.StatusCode), d, 0)
			}
		}(c, codes[i])
	}
	wg.Wait()
	save(cs)
	st.report("signin", map[string]any{"target_rate_per_min": *rate, "attempts": attempts})
}

func short(err error) string {
	s := err.Error()
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// ---- traffic

func cmdTraffic() {
	sink := flag.String("sink", "", "http://ip:port of the sink")
	pct := flag.Float64("pct", 10, "percent of signed-in clients moving bytes each round")
	nbytes := flag.Int("bytes", 262144, "bytes each moving client downloads per round")
	every := flag.Duration("every", 5*time.Second, "round period")
	dur := flag.Duration("duration", time.Minute, "total duration")
	flag.Parse()
	cs := load()
	var live []*Client
	for _, c := range cs {
		if c.SignedIn {
			live = append(live, c)
		}
	}
	hcs := map[int]*http.Client{}
	for _, c := range live {
		hcs[c.Idx] = clientHTTP(c, 20*time.Second)
	}
	st := newStats()
	end := time.Now().Add(*dur)
	rounds := 0
	for time.Now().Before(end) {
		start := time.Now()
		k := int(float64(len(live)) * *pct / 100)
		perm := rand.Perm(len(live))[:k]
		var wg sync.WaitGroup
		for _, pi := range perm {
			c := live[pi]
			wg.Add(1)
			go func() {
				defer wg.Done()
				t := time.Now()
				resp, err := hcs[c.Idx].Get(fmt.Sprintf("%s/blob?n=%d", *sink, *nbytes))
				if err != nil {
					st.add("error:"+short(err), time.Since(t), 0)
					return
				}
				n, _ := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode == 200 {
					st.add("ok", time.Since(t), n)
				} else {
					st.add(fmt.Sprintf("http_%d", resp.StatusCode), time.Since(t), n)
				}
			}()
		}
		wg.Wait()
		rounds++
		if w := *every - time.Since(start); w > 0 {
			time.Sleep(w)
		}
	}
	st.report("traffic", map[string]any{"rounds": rounds, "live_clients": len(live), "pct": *pct, "bytes_per_mover": *nbytes})
}

// ---- release: forget a share of clients so they sign in again with a new voucher

func cmdRelease() {
	pct := flag.Float64("pct", 10, "percent of signed-in clients to drop")
	flag.Parse()
	cs := load()
	k := 0
	for _, c := range cs {
		if c.SignedIn && rand.Float64()*100 < *pct {
			c.SignedIn = false
			k++
		}
	}
	save(cs)
	emit(map[string]any{"phase": "release", "released": k})
}

// ---- sink

func cmdSink() {
	listen := flag.String("listen", ":8080", "listen address")
	flag.Parse()
	var total int64
	buf := make([]byte, 64*1024)
	http.HandleFunc("/blob", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n <= 0 || n > 64<<20 {
			n = 65536
		}
		w.Header().Set("Content-Length", strconv.Itoa(n))
		left := n
		for left > 0 {
			k := len(buf)
			if k > left {
				k = left
			}
			if _, err := w.Write(buf[:k]); err != nil {
				return
			}
			left -= k
		}
		atomic.AddInt64(&total, int64(n))
	})
	http.HandleFunc("/total", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, atomic.LoadInt64(&total)) })
	fatal("sink: %v", http.ListenAndServe(*listen, nil))
}
