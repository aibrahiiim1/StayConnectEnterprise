package nft

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// TEMPORARY VERIFICATION ACCESS, IN THE KERNEL.
//
// One grant is four things in table inet stayconnect (structure rendered by netd, contents owned by scd):
//
//	verify_ipv4     a timed (bridge, address) element -- the admission. The kernel removes it at expiry, so
//	                the window ends on time even if scd is not running.
//	verify_quota    address -> a quota object `over <cap>`: every byte, both directions, counts; past the cap
//	                every packet is dropped.
//	verify_up/down  address -> a limit object `rate over <rate>`: packets beyond the rate are dropped.
//
// The objects are named after the grant (vq_/vu_/vd_ + 12 hex), so a reconcile can tell its own from a stray.
const (
	VerifyV4        = "verify_ipv4"
	VerifyQuotaMap  = "verify_quota"
	VerifyUpMap     = "verify_up"
	VerifyDownMap   = "verify_down"
	verifyObjPrefix = "v"
)

// VerifyGrant is what the kernel needs for one grant.
type VerifyGrant struct {
	Key       string // 12 lower-hex characters, unique per grant (from the grant id)
	Bridge    string
	IP        net.IP
	Remaining time.Duration // what is LEFT of the window; never the original length
	CapBytes  int64
	UsedBytes int64 // carried into the quota object when a grant is restored
	UpKbps    int
	DownKbps  int
}

var verifyKeyRE = regexp.MustCompile(`^[0-9a-f]{12}$`)
var bridgeRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

func verifyNames(key string) (q, up, down string) {
	return verifyObjPrefix + "q_" + key, verifyObjPrefix + "u_" + key, verifyObjPrefix + "d_" + key
}

// kbpsToRate turns kilobits per second into nft's bytes per second, with a burst of half a second (minimum
// 16 kbytes) so a TCP stream is shaped rather than starved.
func kbpsToRate(kbps int) (rate, burst string) {
	bps := int64(kbps) * 1000 / 8
	b := bps / 2
	if b < 16*1024 {
		b = 16 * 1024
	}
	return fmt.Sprintf("%d bytes/second", bps), fmt.Sprintf("%d bytes", b)
}

// VerifyScript is the one nft transaction that installs a grant (deterministic; tested).
func VerifyScript(g VerifyGrant) (string, error) {
	ip := g.IP.To4()
	if !verifyKeyRE.MatchString(g.Key) || ip == nil || !bridgeRE.MatchString(g.Bridge) {
		return "", fmt.Errorf("verify grant: invalid key, address or bridge")
	}
	secs := int(g.Remaining / time.Second)
	if secs < 1 || g.CapBytes <= 0 || g.UpKbps <= 0 || g.DownKbps <= 0 {
		return "", fmt.Errorf("verify grant: nothing left to grant")
	}
	used := g.UsedBytes
	if used < 0 {
		used = 0
	}
	q, up, down := verifyNames(g.Key)
	ur, ub := kbpsToRate(g.UpKbps)
	dr, db := kbpsToRate(g.DownKbps)
	var b strings.Builder
	fmt.Fprintf(&b, "add quota inet stayconnect %s { over %d bytes used %d bytes }\n", q, g.CapBytes, used)
	fmt.Fprintf(&b, "add limit inet stayconnect %s { rate over %s burst %s }\n", up, ur, ub)
	fmt.Fprintf(&b, "add limit inet stayconnect %s { rate over %s burst %s }\n", down, dr, db)
	fmt.Fprintf(&b, "add element inet stayconnect %s { %s : %q }\n", VerifyQuotaMap, ip, q)
	fmt.Fprintf(&b, "add element inet stayconnect %s { %s : %q }\n", VerifyUpMap, ip, up)
	fmt.Fprintf(&b, "add element inet stayconnect %s { %s : %q }\n", VerifyDownMap, ip, down)
	fmt.Fprintf(&b, "add element inet stayconnect %s { %q . %s timeout %ds }\n", VerifyV4, g.Bridge, ip, secs)
	return b.String(), nil
}

// VerifyApply installs a grant in one transaction. Installing a grant whose objects already exist fails as a
// whole and changes nothing; the caller removes first when it means to replace.
func (c *Client) VerifyApply(ctx context.Context, g VerifyGrant) error {
	s, err := VerifyScript(g)
	if err != nil {
		return err
	}
	return c.runScript(ctx, s)
}

// VerifyRemove takes a grant out of the kernel. Each part is removed on its own and a part that is already
// gone is not an error: removal must succeed however much of the grant is left (an expired admission element
// is gone already; a re-render took the objects with it).
func (c *Client) VerifyRemove(ctx context.Context, key, bridge string, ip net.IP) {
	ip4 := ip.To4()
	q, up, down := verifyNames(key)
	var cmds []string
	if ip4 != nil {
		if bridgeRE.MatchString(bridge) {
			cmds = append(cmds, fmt.Sprintf("delete element inet stayconnect %s { %q . %s }", VerifyV4, bridge, ip4))
		}
		cmds = append(cmds,
			fmt.Sprintf("delete element inet stayconnect %s { %s }", VerifyQuotaMap, ip4),
			fmt.Sprintf("delete element inet stayconnect %s { %s }", VerifyUpMap, ip4),
			fmt.Sprintf("delete element inet stayconnect %s { %s }", VerifyDownMap, ip4))
	}
	if verifyKeyRE.MatchString(key) {
		cmds = append(cmds,
			fmt.Sprintf("delete quota inet stayconnect %s", q),
			fmt.Sprintf("delete limit inet stayconnect %s", up),
			fmt.Sprintf("delete limit inet stayconnect %s", down))
	}
	for _, cmd := range cmds {
		_ = c.runScript(ctx, cmd+"\n")
	}
}

// VerifyState is what the kernel holds: the live admission elements, and the bytes each grant's quota object
// has counted (by grant key).
type VerifyState struct {
	Admitted map[string]bool  // "bridge|ip"
	Used     map[string]int64 // grant key -> bytes counted
	Objects  map[string]bool  // grant keys that have a quota object
}

// VerifyRead reads the admission set and the quota objects. An error means the state is unknown, not empty.
func (c *Client) VerifyRead(ctx context.Context) (VerifyState, error) {
	st := VerifyState{Admitted: map[string]bool{}, Used: map[string]int64{}, Objects: map[string]bool{}}
	raw, err := exec.CommandContext(ctx, c.NftPath, "-j", "list", "set", "inet", "stayconnect", VerifyV4).Output()
	if err != nil {
		return st, fmt.Errorf("nft list %s: %w", VerifyV4, err)
	}
	els, err := parseJSON(raw)
	if err != nil {
		return st, err
	}
	for _, e := range els {
		st.Admitted[e.Iface+"|"+e.IP.String()] = true
	}
	qraw, err := exec.CommandContext(ctx, c.NftPath, "-j", "list", "quotas", "table", "inet", "stayconnect").Output()
	if err != nil {
		return st, fmt.Errorf("nft list quotas: %w", err)
	}
	used, err := parseVerifyQuotas(qraw)
	if err != nil {
		return st, err
	}
	for k, v := range used {
		st.Used[k] = v
		st.Objects[k] = true
	}
	return st, nil
}

func parseVerifyQuotas(raw []byte) (map[string]int64, error) {
	var doc struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, o := range doc.Nftables {
		qr, ok := o["quota"]
		if !ok {
			continue
		}
		var q struct {
			Name string `json:"name"`
			Used int64  `json:"used"`
		}
		if json.Unmarshal(qr, &q) != nil {
			continue
		}
		if strings.HasPrefix(q.Name, verifyObjPrefix+"q_") && verifyKeyRE.MatchString(strings.TrimPrefix(q.Name, verifyObjPrefix+"q_")) {
			out[strings.TrimPrefix(q.Name, verifyObjPrefix+"q_")] = q.Used
		}
	}
	return out, nil
}
