package main

// A stateful simulation of the tc and nft command line, so the REAL internal/shape and internal/nft clients (and every command string they
// build) run unchanged against it. It counts every invocation, which is what the scaling work is measured by: on the appliance the cost of a
// pass is the number of forked tc/nft processes, each 10-25 ms, not the work the kernel does.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/nft"
	"github.com/stayconnect/enterprise/data-plane/internal/shape"
)

type fakeKClass struct {
	parent            string
	rateKbit, ceilKbt int
	leaf              bool
	bytes             uint64
}

type fakeKDev struct {
	classes map[int]*fakeKClass
	filters map[int]string // pref -> flowid
}

type fakeKElem struct{ expires time.Time }

// execCounts is what a harness reports.
type execCounts struct {
	TC, NFT, IP           int
	NFTList, NFTWrite     int
	TCClassChange         int
	TCClassShow           int
	TCBatch, NFTScript    int
	BatchLines, ScriptOps int
}

func (c execCounts) total() int { return c.TC + c.NFT + c.IP }

func (c execCounts) sub(o execCounts) execCounts {
	return execCounts{c.TC - o.TC, c.NFT - o.NFT, c.IP - o.IP, c.NFTList - o.NFTList, c.NFTWrite - o.NFTWrite,
		c.TCClassChange - o.TCClassChange, c.TCClassShow - o.TCClassShow, c.TCBatch - o.TCBatch, c.NFTScript - o.NFTScript,
		c.BatchLines - o.BatchLines, c.ScriptOps - o.ScriptOps}
}

type kernelFake struct {
	mu   sync.Mutex
	now  time.Time
	devs map[string]*fakeKDev
	sets map[string]map[string]*fakeKElem
	cnt  execCounts

	// failTC / failNFT inject a failure for a command (single or one batch line / script op); nil = none.
	failTC  func(args []string) error
	failNFT func(op string) error
	// perExec is a simulated fork cost, applied to wall time only when set.
	perExec time.Duration
	// opLog is every committed nft element operation in order: "add|del set key".
	opLog []string
}

func newKernelFake(now time.Time) *kernelFake {
	return &kernelFake{now: now, devs: map[string]*fakeKDev{}, sets: map[string]map[string]*fakeKElem{
		"auth_ipv4": {}, "phase3_auth_ipv4": {}, "phase3_auth_mac_ipv4": {}}}
}

func (k *kernelFake) dev(name string) *fakeKDev {
	d, ok := k.devs[name]
	if !ok {
		d = &fakeKDev{classes: map[int]*fakeKClass{}, filters: map[int]string{}}
		k.devs[name] = d
	}
	return d
}

func (k *kernelFake) advance(d time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.now = k.now.Add(d)
	for _, s := range k.sets {
		for key, e := range s {
			if !e.expires.After(k.now) {
				delete(s, key)
			}
		}
	}
}

func (k *kernelFake) counts() execCounts {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.cnt
}

func (k *kernelFake) setLen(set string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.sets[set])
}

func (k *kernelFake) hasElem(set, key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.sets[set][normKey(key)]
	return ok
}

func (k *kernelFake) classCount(dev string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.dev(dev).classes)
}

func normKey(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), " ", "") }

func (k *kernelFake) shapeClient() *shape.Client {
	return shape.NewWithExec(k.exec, k.execIn)
}

func (k *kernelFake) nftClient() *nft.Client { return nft.NewWithExec(k.exec, k.execIn) }

func (k *kernelFake) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.perExec > 0 {
		time.Sleep(k.perExec)
	}
	return k.dispatch(name, args, nil)
}

func (k *kernelFake) execIn(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.perExec > 0 {
		time.Sleep(k.perExec)
	}
	return k.dispatch(name, args, stdin)
}

func (k *kernelFake) dispatch(name string, args []string, stdin []byte) ([]byte, error) {
	base := name[strings.LastIndex(name, "/")+1:]
	switch base {
	case "ip":
		k.cnt.IP++
		return nil, nil
	case "tc":
		k.cnt.TC++
		return k.tc(args, stdin)
	case "nft":
		k.cnt.NFT++
		return k.nft(args, stdin)
	}
	return nil, fmt.Errorf("fake kernel: unexpected command %s", name)
}

// ---- tc ----

func (k *kernelFake) tc(args []string, stdin []byte) ([]byte, error) {
	force := false
	if len(args) > 0 && args[0] == "-force" {
		force, args = true, args[1:]
	}
	if len(args) >= 2 && args[0] == "-batch" {
		k.cnt.TCBatch++
		var failed []string
		for i, line := range strings.Split(string(stdin), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			k.cnt.BatchLines++
			if out, err := k.tcOne(strings.Fields(line)); err != nil {
				failed = append(failed, fmt.Sprintf("%s\nCommand failed -:%d", strings.TrimSpace(string(out)), i+1))
				if !force {
					break
				}
			}
		}
		if len(failed) > 0 {
			return []byte(strings.Join(failed, "\n")), fmt.Errorf("exit status 1")
		}
		return nil, nil
	}
	return k.tcOne(args)
}

func minorOf(cid string) int {
	i := strings.Index(cid, ":")
	if i < 0 {
		return -1
	}
	v, err := strconv.ParseInt(cid[i+1:], 16, 32)
	if err != nil {
		return -1
	}
	return int(v)
}

func argAfter(args []string, key string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key {
			return args[i+1]
		}
	}
	return ""
}

func kbit(s string) int {
	s = strings.TrimSuffix(s, "kbit")
	v, _ := strconv.Atoi(s)
	return v
}

func (k *kernelFake) tcOne(args []string) ([]byte, error) {
	if k.failTC != nil {
		if err := k.failTC(args); err != nil {
			return []byte(err.Error()), err
		}
	}
	if len(args) >= 4 && args[0] == "-s" && args[1] == "class" && args[2] == "show" {
		k.cnt.TCClassShow++
		return k.renderClasses(args[4]), nil
	}
	if len(args) < 3 {
		return nil, fmt.Errorf("fake tc: short command %v", args)
	}
	obj, verb := args[0], args[1]
	dev := argAfter(args, "dev")
	switch obj + " " + verb {
	case "class add", "class change":
		cid := argAfter(args, "classid")
		m := minorOf(cid)
		d := k.dev(dev)
		cur, exists := d.classes[m]
		if verb == "add" && exists {
			return []byte("RTNETLINK answers: File exists"), fmt.Errorf("exit status 2")
		}
		if verb == "change" {
			k.cnt.TCClassChange++
			if !exists {
				return []byte("RTNETLINK answers: No such file or directory"), fmt.Errorf("exit status 2")
			}
		}
		c := &fakeKClass{parent: argAfter(args, "parent"), rateKbit: kbit(argAfter(args, "rate")), ceilKbt: kbit(argAfter(args, "ceil"))}
		if cur != nil {
			c.leaf, c.bytes = cur.leaf, cur.bytes
		}
		d.classes[m] = c
	case "class del":
		m := minorOf(argAfter(args, "classid"))
		d := k.dev(dev)
		if _, ok := d.classes[m]; !ok {
			return []byte("RTNETLINK answers: No such file or directory"), fmt.Errorf("exit status 2")
		}
		for _, fl := range d.filters {
			if minorOf(fl) == m {
				return []byte("RTNETLINK answers: Device or resource busy"), fmt.Errorf("exit status 2")
			}
		}
		delete(d.classes, m)
	case "qdisc add", "qdisc del":
		parent := argAfter(args, "parent")
		if parent == "" || parent == "ffff:" || parent == "root" {
			return nil, nil // root / ingress infrastructure
		}
		m := minorOf(parent)
		c, ok := k.dev(dev).classes[m]
		if !ok {
			return []byte("Error: Parent Qdisc doesn't exists."), fmt.Errorf("exit status 2")
		}
		if verb == "add" {
			if c.leaf {
				return []byte("RTNETLINK answers: File exists"), fmt.Errorf("exit status 2")
			}
			c.leaf = true
		} else {
			if !c.leaf {
				return []byte("RTNETLINK answers: No such file or directory"), fmt.Errorf("exit status 2")
			}
			c.leaf = false
		}
	case "filter add":
		pref, _ := strconv.Atoi(argAfter(args, "pref"))
		d := k.dev(dev)
		if argAfter(args, "parent") == "ffff:" { // ingress redirect
			return nil, nil
		}
		if _, ok := d.filters[pref]; ok {
			return []byte("RTNETLINK answers: File exists"), fmt.Errorf("exit status 2")
		}
		d.filters[pref] = argAfter(args, "flowid")
	case "filter del":
		pref, _ := strconv.Atoi(argAfter(args, "pref"))
		d := k.dev(dev)
		if _, ok := d.filters[pref]; !ok {
			return []byte("RTNETLINK answers: No such file or directory"), fmt.Errorf("exit status 2")
		}
		delete(d.filters, pref)
	case "filter show":
		var b strings.Builder
		for _, pref := range sortedPrefs(k.dev(dev).filters) {
			fmt.Fprintf(&b, "filter protocol ip pref %d u32 chain 0 \nfilter protocol ip pref %d u32 chain 0 fh 800: ht divisor 1 \n", pref, pref)
		}
		return []byte(b.String()), nil
	default:
		return nil, fmt.Errorf("fake tc: unsupported %v", args)
	}
	return nil, nil
}

func sortedPrefs(m map[int]string) []int {
	out := make([]int, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

func fmtRate(kb int) string {
	switch {
	case kb%1000000 == 0:
		return fmt.Sprintf("%dGbit", kb/1000000)
	case kb%1000 == 0:
		return fmt.Sprintf("%dMbit", kb/1000)
	}
	return fmt.Sprintf("%dKbit", kb)
}

func (k *kernelFake) renderClasses(dev string) []byte {
	d := k.dev(dev)
	minors := make([]int, 0, len(d.classes))
	for m := range d.classes {
		minors = append(minors, m)
	}
	sort.Ints(minors)
	var b strings.Builder
	for _, m := range minors {
		c := d.classes[m]
		par := "root"
		if c.parent != "1:" {
			par = "parent " + c.parent
		}
		leaf := ""
		if c.leaf {
			leaf = fmt.Sprintf(" leaf %x:", m)
		}
		fmt.Fprintf(&b, "class htb 1:%x %s%s prio 0 rate %s ceil %s burst 32Kb cburst 1600b \n Sent %d bytes %d pkt (dropped 0, overlimits 0 requeues 0) \n backlog 0b 0p requeues 0\n lended: 0 borrowed: 0 giants: 0\n tokens: 1 ctokens: 1\n\n",
			m, par, leaf, fmtRate(c.rateKbit), fmtRate(c.ceilKbt), c.bytes, c.bytes/1000)
	}
	return []byte(b.String())
}

// ---- nft ----

func (k *kernelFake) nft(args []string, stdin []byte) ([]byte, error) {
	switch {
	case len(args) == 5 && args[0] == "list" && args[1] == "set":
		set := args[4]
		if _, ok := k.sets[set]; !ok {
			return []byte("Error: No such file or directory"), fmt.Errorf("exit status 1")
		}
		k.cnt.NFTList++
		typ := "ifname . ipv4_addr"
		if set == "phase3_auth_mac_ipv4" {
			typ = "ifname . ipv4_addr . ether_addr"
		}
		return []byte(fmt.Sprintf("table inet stayconnect {\n\tset %s {\n\t\ttype %s\n\t\tflags timeout\n\t}\n}\n", set, typ)), nil
	case len(args) == 6 && args[0] == "-j" && args[1] == "list" && args[2] == "set":
		set := args[5]
		s, ok := k.sets[set]
		if !ok {
			return []byte("Error: No such file or directory"), fmt.Errorf("exit status 1")
		}
		k.cnt.NFTList++
		return k.renderSet(set, s), nil
	case len(args) >= 2 && args[0] == "-f" && args[1] == "-":
		k.cnt.NFTScript++
		k.cnt.NFTWrite++
		return k.nftScript(string(stdin))
	default:
		k.cnt.NFTWrite++
		return k.nftScript(strings.Join(args, " "))
	}
}

func (k *kernelFake) renderSet(set string, s map[string]*fakeKElem) []byte {
	keys := make([]string, 0, len(s))
	for key := range s {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var elems []any
	for _, key := range keys {
		var concat []any
		for _, part := range strings.Split(key, ".") {
			_ = part
		}
		// key is "iface".a.b.c.d[.mac]; rebuild the concat parts from the stored form
		concat = fkSplitKey(key)
		left := int(s[key].expires.Sub(k.now) / time.Second)
		elems = append(elems, map[string]any{"elem": map[string]any{"val": map[string]any{"concat": concat}, "timeout": 90, "expires": left}})
	}
	out, _ := json.Marshal(map[string]any{"nftables": []any{
		map[string]any{"metainfo": map[string]any{"version": "1.0.2"}},
		map[string]any{"set": map[string]any{"family": "inet", "name": set, "table": "stayconnect", "elem": elems}}}})
	return out
}

// splitKey turns the normalised key "\"br\".10.0.0.1[.aa:bb:..]" back into its concat parts.
func fkSplitKey(key string) []any {
	end := strings.LastIndex(key, "\"")
	iface := strings.Trim(key[:end+1], "\"")
	rest := strings.TrimPrefix(key[end+1:], ".")
	parts := []any{iface}
	// the IPv4 address is the first four dotted fields; anything after is a MAC
	f := strings.SplitN(rest, ".", 5)
	parts = append(parts, strings.Join(f[:4], "."))
	if len(f) == 5 {
		parts = append(parts, f[4])
	}
	return parts
}

// nftScript applies the commands of one nft invocation as ONE transaction: a failing command rolls everything back, as the kernel does.
func (k *kernelFake) nftScript(script string) ([]byte, error) {
	script = strings.ReplaceAll(script, "\n", ";")
	type edit struct {
		set, key string
		add      bool
		ttl      time.Duration
	}
	var edits []edit
	for _, cmd := range strings.Split(script, ";") {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			continue
		}
		k.cnt.ScriptOps++
		if k.failNFT != nil {
			if err := k.failNFT(cmd); err != nil {
				return []byte(err.Error()), err
			}
		}
		f := strings.Fields(cmd)
		if len(f) < 7 || f[1] != "element" || f[2] != "inet" || f[3] != "stayconnect" {
			return []byte("Error: unsupported " + cmd), fmt.Errorf("exit status 1")
		}
		set := f[4]
		open, closeI := strings.Index(cmd, "{"), strings.LastIndex(cmd, "}")
		body := strings.TrimSpace(cmd[open+1 : closeI])
		e := edit{set: set, add: f[0] == "add"}
		if i := strings.Index(body, " timeout "); i >= 0 {
			secs, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(body[i+len(" timeout "):]), "s"))
			e.ttl = time.Duration(secs) * time.Second
			body = body[:i]
		}
		e.key = normKey(body)
		edits = append(edits, e)
	}
	// stage on a copy
	stage := map[string]map[string]*fakeKElem{}
	get := func(set string) (map[string]*fakeKElem, bool) {
		if m, ok := stage[set]; ok {
			return m, true
		}
		orig, ok := k.sets[set]
		if !ok {
			return nil, false
		}
		c := make(map[string]*fakeKElem, len(orig))
		for kk, v := range orig {
			c[kk] = v
		}
		stage[set] = c
		return c, true
	}
	for _, e := range edits {
		m, ok := get(e.set)
		if !ok {
			return []byte("Error: No such file or directory"), fmt.Errorf("exit status 1")
		}
		if e.add {
			if _, exists := m[e.key]; !exists {
				m[e.key] = &fakeKElem{expires: k.now.Add(e.ttl)}
			}
			continue
		}
		if _, exists := m[e.key]; !exists {
			return []byte("Error: Could not process rule: No such file or directory"), fmt.Errorf("exit status 1")
		}
		delete(m, e.key)
	}
	for _, e := range edits {
		op := "del"
		if e.add {
			op = "add"
		}
		k.opLog = append(k.opLog, op+" "+e.set+" "+e.key)
	}
	for set, m := range stage {
		k.sets[set] = m
	}
	return nil, nil
}

// ---- helpers shared by the scale tests ----

type nopFlusher struct {
	mu      sync.Mutex
	flushed int
	calls   int
}

func (f *nopFlusher) FlushAddrs(ctx context.Context, ips []net.IP) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushed += len(ips)
	f.calls++
	return len(ips), nil
}

// realExecCounting runs the real binaries and counts them, so the same scenario can be measured against a real kernel in a privileged container.
type realKernel struct {
	mu  sync.Mutex
	cnt execCounts
}

func (r *realKernel) count(name string, args []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	base := name[strings.LastIndex(name, "/")+1:]
	switch base {
	case "tc":
		r.cnt.TC++
		j := strings.Join(args, " ")
		if strings.Contains(j, "class change") {
			r.cnt.TCClassChange++
		}
		if strings.HasPrefix(j, "-s class show") {
			r.cnt.TCClassShow++
		}
		if strings.Contains(j, "-batch") {
			r.cnt.TCBatch++
		}
	case "nft":
		r.cnt.NFT++
		j := strings.Join(args, " ")
		if strings.Contains(j, "list set") {
			r.cnt.NFTList++
		} else {
			r.cnt.NFTWrite++
		}
		if strings.HasPrefix(j, "-f") {
			r.cnt.NFTScript++
		}
	default:
		r.cnt.IP++
	}
}

func (r *realKernel) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.count(name, args)
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (r *realKernel) execIn(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	r.count(name, args)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(string(stdin))
	return cmd.CombinedOutput()
}

func (r *realKernel) counts() execCounts {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cnt
}
