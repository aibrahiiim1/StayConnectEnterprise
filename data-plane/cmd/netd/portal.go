package main

// Portal address and HTTPS state on the wire.
// The portal address is network configuration (validate, apply, confirm; recorded in the revision bundle). HTTPS state is runtime state: its refresh never touches nftables or netplan, so it cannot change what guests may reach.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// portalRefreshInterval is not an operator setting; it bounds how soon a device's DHCP answer reflects scd's decision.
const portalRefreshInterval = 15 * time.Second

type keaRuntime interface {
	RunningDhcp4() (map[string]any, error)
	ConfigTest(dhcp4 map[string]any) error
	ConfigSet(dhcp4 map[string]any) error
}

// portalInForce is what the wire carries now; every render reads it so apply, rollback and refresh render the same thing.
type portalInForce struct {
	intent []netcfg.GuestNetwork
	vip    string
	known  bool
	// activeVIP is the confirmed active revision's portal address, the rollback target when the previous bundle records none.
	activeVIP string
	tls       netcfg.PortalTLS

	// Log-once bookkeeping so a 15 s refresh does not repeat a line.
	lastOpt114 string
	lastErr    map[string]string
}

func (a *applier) renderTopo() netcfg.Topology {
	a.portalMu.Lock()
	defer a.portalMu.Unlock()
	t := a.topo
	t.MgmtAddr = a.resolveMgmtAddr(t.MgmtAddr)
	t.PortalVIP = a.portal.vip
	t.PortalTLS = a.portal.tls
	a.applyPolicy(&t)
	return t
}

func (a *applier) setInForce(intent []netcfg.GuestNetwork, vip string) {
	a.portalMu.Lock()
	defer a.portalMu.Unlock()
	a.portal.intent, a.portal.vip, a.portal.known = intent, vip, true
}

func (a *applier) clearInForce() {
	a.portalMu.Lock()
	defer a.portalMu.Unlock()
	a.portal.intent, a.portal.vip, a.portal.known, a.portal.activeVIP = nil, "", false, ""
}

func (a *applier) inForce() ([]netcfg.GuestNetwork, string, bool) {
	a.portalMu.Lock()
	defer a.portalMu.Unlock()
	return a.portal.intent, a.portal.vip, a.portal.known
}

func (a *applier) snapshotInForce() portalInForce {
	a.portalMu.Lock()
	defer a.portalMu.Unlock()
	return a.portal
}

func (a *applier) restoreInForce(p portalInForce) {
	a.portalMu.Lock()
	defer a.portalMu.Unlock()
	a.portal.intent, a.portal.vip, a.portal.known = p.intent, p.vip, p.known
}

func (a *applier) setActiveVIP(vip string) {
	a.portalMu.Lock()
	defer a.portalMu.Unlock()
	a.portal.activeVIP = vip
}

func (a *applier) activeVIP() string {
	a.portalMu.Lock()
	defer a.portalMu.Unlock()
	return a.portal.activeVIP
}

func (a *applier) setPortalTLS(p netcfg.PortalTLS) {
	a.portalMu.Lock()
	defer a.portalMu.Unlock()
	a.portal.tls = p
}

func (a *applier) logOnce(key, msg string) {
	a.portalMu.Lock()
	if a.portal.lastErr == nil {
		a.portal.lastErr = map[string]string{}
	}
	same := a.portal.lastErr[key] == msg
	a.portal.lastErr[key] = msg
	a.portalMu.Unlock()
	if msg != "" && !same {
		slog.Error("netd portal refresh", "step", key, "err", msg)
	}
}

func (a *applier) loadPortalIP(ctx context.Context) (string, error) {
	if a.portalIPFn != nil {
		return a.portalIPFn(ctx)
	}
	if a.st == nil {
		return "", nil
	}
	return a.st.LoadPortalIP(ctx)
}

func (a *applier) loadPortalTLS(ctx context.Context) (netcfg.PortalTLS, error) {
	if a.portalTLSFn != nil {
		return a.portalTLSFn(ctx)
	}
	if a.st == nil {
		return netcfg.PortalTLS{}, nil
	}
	return a.st.LoadPortalTLS(ctx)
}

// On a read failure the previous HTTPS state is kept: flipping option 114 on a transient error would cause the outage it reports.
func (a *applier) refreshPortalTLS(ctx context.Context) {
	p, err := a.loadPortalTLS(ctx)
	if err != nil {
		a.logOnce("tls_state", "portal HTTPS state unreadable; keeping the last known state: "+err.Error())
		return
	}
	a.logOnce("tls_state", "")
	a.setPortalTLS(p)
}

// hostAddrs lists every IPv4 address including Docker and guest bridges, which the portal address must not overlap.
func (a *applier) hostAddrs(ctx context.Context) ([]netcfg.HostAddr, error) {
	if a.hostAddrsFn != nil {
		return a.hostAddrsFn(ctx)
	}
	out, err := a.output(ctx, "ip", "-j", "-4", "addr")
	if err != nil {
		return nil, fmt.Errorf("list host addresses: %w", err)
	}
	return parseHostAddrs(out)
}

func parseHostAddrs(raw []byte) ([]netcfg.HostAddr, error) {
	var entries []ipAddrEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse host addresses: %w", err)
	}
	var out []netcfg.HostAddr
	for _, e := range entries {
		for _, ad := range e.AddrInfo {
			if ad.Family == "inet" && ad.Local != "" {
				out = append(out, netcfg.HostAddr{Iface: e.IfName, CIDR: ad.Local + "/" + strconv.Itoa(ad.PrefixLen)})
			}
		}
	}
	return out, nil
}

func (a *applier) portalLink(name string) (exists, up bool) {
	if a.portalLinkFn != nil {
		return a.portalLinkFn(name)
	}
	raw, err := os.ReadFile("/sys/class/net/" + name + "/flags")
	if err != nil {
		return ifaceExists(name), false
	}
	flags, perr := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(string(raw)), "0x"), 16, 32)
	return true, perr == nil && flags&0x1 != 0 // IFF_UP
}

func (a *applier) keaRT() keaRuntime {
	if a.keaRTSeam != nil {
		return a.keaRTSeam
	}
	if a.kea != nil {
		return a.kea
	}
	return nil
}

// validatePortal fails closed: if host addresses cannot be read, overlap cannot be ruled out.
func (a *applier) validatePortal(ctx context.Context, vip string, intent []netcfg.GuestNetwork) ([]netcfg.Issue, error) {
	if strings.TrimSpace(vip) == "" {
		return nil, nil
	}
	host, err := a.hostAddrs(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot establish which networks this appliance carries, so the portal address "+
			"cannot be checked for overlap: %w -- refusing rather than guessing", err)
	}
	return netcfg.ValidatePortalIP(vip, intent, host), nil
}

// ValidatePortalIP answers POST /v1/portal-ip/validate; the apply validates again at apply time.
func (a *applier) ValidatePortalIP(ctx context.Context, vip string) (netcfg.ValidationResult, error) {
	res := netcfg.ValidationResult{OK: true, Issues: []netcfg.Issue{}}
	if strings.TrimSpace(vip) == "" {
		res.OK = false
		res.Issues = append(res.Issues, netcfg.Issue{Field: "portal_ip", Code: "required",
			Message: "a portal address is required"})
		return res, nil
	}
	var intent []netcfg.GuestNetwork
	if a.intentFn != nil {
		var err error
		if intent, err = a.intentFn(ctx); err != nil {
			return res, err
		}
	} else if a.st != nil {
		var err error
		if intent, err = a.st.LoadIntent(ctx); err != nil {
			return res, err
		}
	}
	issues, err := a.validatePortal(ctx, vip, intent)
	if err != nil {
		return res, err
	}
	if len(issues) > 0 {
		res.OK = false
		res.Issues = append(res.Issues, issues...)
	}
	return res, nil
}

// An empty file records "none"; a missing file means the revision predates the portal address.
func writePortalBundle(bundle, vip string) error {
	return os.WriteFile(filepath.Join(bundle, netcfg.PortalVIPBundleFile), []byte(strings.TrimSpace(vip)+"\n"), 0o640)
}

func readPortalBundle(bundle string) (vip string, recorded bool) {
	if bundle == "" {
		return "", false
	}
	raw, err := os.ReadFile(filepath.Join(bundle, netcfg.PortalVIPBundleFile))
	if err != nil {
		return "", false
	}
	v := strings.TrimSpace(string(raw))
	if v != "" && netcfg.ParsePortalIP(v) == nil {
		// A record that is not an address is read as "none": rendering it would break the whole ruleset.
		slog.Error("netd: a revision bundle records an unusable portal address; treating it as none",
			"bundle", bundle, "value", v)
		return "", true
	}
	return v, true
}

// adoptPortalIP upgrades a confirmed revision that predates the portal address, adopting the configured address only if it passes apply validation.
// Anything less adopts nothing: adopting an unvalidated address on an unattended restart could steal an address from a real network.
func (a *applier) adoptPortalIP(ctx context.Context, bundle string, intent []netcfg.GuestNetwork) string {
	vip, err := a.loadPortalIP(ctx)
	if err != nil {
		slog.Error("netd: the portal address setting is unreadable; no portal address adopted", "err", err)
		return ""
	}
	if vip == "" {
		return ""
	}
	issues, err := a.validatePortal(ctx, vip, intent)
	if err != nil || len(issues) > 0 {
		slog.Error("netd: the configured portal address was NOT adopted for a revision that predates it; apply "+
			"the network configuration to put a valid one in force", "portal_ip", vip, "err", err, "issues", issues)
		return ""
	}
	if bundle != "" {
		if _, statErr := os.Stat(bundle); statErr == nil {
			if werr := writePortalBundle(bundle, vip); werr != nil {
				slog.Warn("netd: adopted portal address could not be recorded in the active bundle", "err", werr)
			}
		}
	}
	slog.Info("netd: adopted the configured portal address for the active revision", "portal_ip", vip)
	return vip
}

// ensurePortalIface is a no-op when the interface is already right, so the refresh is silent.
func (a *applier) ensurePortalIface(ctx context.Context, vip string) (bool, error) {
	if a.dryRun {
		return false, nil
	}
	name := netcfg.PortalIface
	exists, up := a.portalLink(name)
	ip := netcfg.ParsePortalIP(vip)
	if ip == nil {
		if !exists {
			return false, nil
		}
		if err := a.run(ctx, "ip", "link", "del", name); err != nil {
			return true, fmt.Errorf("remove the portal interface: %w", err)
		}
		return true, nil
	}
	changed := false
	if !exists {
		if err := a.run(ctx, "ip", "link", "add", name, "type", "dummy"); err != nil {
			return true, fmt.Errorf("create the portal interface: %w", err)
		}
		changed = true
	}
	want := ip.String() + "/32"
	found := false
	for _, c := range a.ifaceAddrsOf(name) {
		if c == want {
			found = true
			continue
		}
		// The previous address goes: an interface carrying both would answer for an address no ruleset admits.
		changed = true
		if err := a.run(ctx, "ip", "addr", "del", c, "dev", name); err != nil {
			return true, fmt.Errorf("remove stale portal address %s: %w", c, err)
		}
	}
	if !found {
		changed = true
		if err := a.run(ctx, "ip", "addr", "replace", want, "dev", name); err != nil {
			return true, fmt.Errorf("assign portal address %s: %w", want, err)
		}
	}
	if !exists || !up {
		changed = true
		if err := a.run(ctx, "ip", "link", "set", name, "up"); err != nil {
			return true, fmt.Errorf("bring the portal interface up: %w", err)
		}
	}
	return changed, nil
}

func (a *applier) portalIfaceHealthy(vip string) (bool, string) {
	ip := netcfg.ParsePortalIP(vip)
	exists, up := a.portalLink(netcfg.PortalIface)
	if ip == nil {
		if exists {
			return false, netcfg.PortalIface + " exists but no portal address is configured"
		}
		return true, ""
	}
	if !exists || !up {
		return false, netcfg.PortalIface + " is missing or down"
	}
	for _, c := range a.ifaceAddrsOf(netcfg.PortalIface) {
		if c == ip.String()+"/32" {
			return true, ""
		}
	}
	return false, netcfg.PortalIface + " does not carry " + ip.String() + "/32"
}

// writeUnboundPortal writes atomically: a resolver reading a half-written file would refuse its whole configuration.
func (a *applier) writeUnboundPortal(vip string, p netcfg.PortalTLS) (bool, []byte, error) {
	if a.dryRun || a.unboundPortalFrag == "" {
		return false, nil, nil
	}
	want := netcfg.RenderUnboundPortal(vip, p)
	old, err := os.ReadFile(a.unboundPortalFrag)
	if err == nil && string(old) == string(want) {
		return false, old, nil
	}
	if err != nil {
		old = nil
	}
	tmp := a.unboundPortalFrag + ".tmp"
	if err := os.WriteFile(tmp, want, 0o644); err != nil {
		return false, old, fmt.Errorf("write portal resolver fragment: %w", err)
	}
	if err := os.Rename(tmp, a.unboundPortalFrag); err != nil {
		_ = os.Remove(tmp)
		return false, old, fmt.Errorf("install portal resolver fragment: %w", err)
	}
	return true, old, nil
}

// A fragment the resolver refuses is restored, so a bad hostname cannot take guest DNS down.
func (a *applier) reloadUnboundPortal(ctx context.Context, previous []byte) error {
	if a.dryRun {
		return nil
	}
	if err := a.run(ctx, "unbound-checkconf"); err != nil {
		if previous != nil {
			_ = os.WriteFile(a.unboundPortalFrag, previous, 0o644)
		} else {
			_ = os.Remove(a.unboundPortalFrag)
		}
		return fmt.Errorf("the resolver refused the portal fragment, which was put back: %w", err)
	}
	// local-data only: reload suffices.
	if err := a.run(ctx, "unbound-control", "reload"); err != nil {
		if rerr := a.run(ctx, "systemctl", "restart", "unbound"); rerr != nil {
			return fmt.Errorf("the resolver would neither reload (%v) nor restart: %w", err, rerr)
		}
	}
	return nil
}

// renderKea is the single Kea render so apply, rollback, boot and refresh agree on option 114.
func (a *applier) renderKea(intent []netcfg.GuestNetwork) map[string]any {
	return netcfg.RenderKeaDhcp4(intent, a.renderTopo(), a.keaLeaseCSV, a.keaSocket)
}

func (a *applier) pushKea(dhcp4 map[string]any) error {
	a.keaMu.Lock()
	defer a.keaMu.Unlock()
	kr := a.keaRT()
	if kr == nil {
		return errors.New("no Kea control socket configured")
	}
	return kr.ConfigSet(dhcp4)
}

// captiveOptions compares render and config-get via JSON so both compare like for like.
func captiveOptions(dhcp4 map[string]any) map[string]string {
	raw, err := json.Marshal(dhcp4)
	if err != nil {
		return nil
	}
	var d struct {
		Subnet4 []struct {
			Subnet  string `json:"subnet"`
			Options []struct {
				Name string `json:"name"`
				Code int    `json:"code"`
				Data string `json:"data"`
			} `json:"option-data"`
		} `json:"subnet4"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil
	}
	out := map[string]string{}
	for _, s := range d.Subnet4 {
		v := ""
		for _, o := range s.Options {
			// Code 114 only: a stale "v4-captive-portal" entry is code 160 on Kea 2.0 and must read as no option 114.
			if o.Code == 114 {
				v = o.Data
			}
		}
		out[s.Subnet] = v
	}
	return out
}

func describeOptions(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, " ")
}

// syncKeaCaptive pushes only when running option-114 values differ from the HTTPS state; it creates no revision and is config-tested first.
// It holds the Kea lock across read-compare-push and yields to an apply, rollback or boot reconcile in progress.
func (a *applier) syncKeaCaptive(ctx context.Context) (bool, error) {
	if a.dryRun {
		return false, nil
	}
	// Only while no apply, rollback or boot reconcile is running: the in-force intent is read under the operation lock, so it can never be one an
	// apply has published but not yet proven.
	if !a.opMu.TryLock() {
		return false, nil
	}
	defer a.opMu.Unlock()
	return a.syncKeaCaptiveLocked(ctx)
}

// syncKeaCaptiveLocked is syncKeaCaptive for a caller that already holds the operation lock.
func (a *applier) syncKeaCaptiveLocked(ctx context.Context) (bool, error) {
	if a.dryRun {
		return false, nil
	}
	a.keaMu.Lock()
	defer a.keaMu.Unlock()
	if a.busy.Load() > 0 {
		return false, nil
	}
	intent, _, known := a.inForce()
	if !known || len(intent) == 0 {
		return false, nil
	}
	kr := a.keaRT()
	if kr == nil {
		return false, nil
	}
	running, err := kr.RunningDhcp4()
	if err != nil {
		// Kea not running is not this loop's problem.
		return false, fmt.Errorf("read the running DHCP configuration: %w", err)
	}
	desired := a.renderKea(intent)
	want, have := captiveOptions(desired), captiveOptions(running)
	if reflect.DeepEqual(want, have) {
		return false, nil
	}
	if err := kr.ConfigTest(desired); err != nil {
		return false, err
	}
	if err := kr.ConfigSet(desired); err != nil {
		return false, err
	}
	if d := describeOptions(want); d != a.lastOpt114() {
		a.setLastOpt114(d)
		slog.Info("netd: DHCP option 114 follows the portal's HTTPS state", "options", d, "was", describeOptions(have))
	}
	return true, nil
}

func (a *applier) lastOpt114() string {
	a.portalMu.Lock()
	defer a.portalMu.Unlock()
	return a.portal.lastOpt114
}

func (a *applier) setLastOpt114(s string) {
	a.portalMu.Lock()
	defer a.portalMu.Unlock()
	a.portal.lastOpt114 = s
}

// Refresh steps are independent; one failing does not stop the others.
func (a *applier) portalRefresh(ctx context.Context) {
	if a.dryRun || a.busy.Load() > 0 {
		return
	}
	if !a.opMu.TryLock() {
		return
	}
	defer a.opMu.Unlock()
	// Retry saving the confirmed DHCP configuration to disk if the last attempt failed (read-only filesystem, full disk, wrong owner).
	if ok, _ := a.keaDiskState(); !ok {
		_ = a.persistConfirmedKea(ctx, "")
	}
	a.refreshPortalTLS(ctx)
	_, vip, known := a.inForce()
	if !known {
		// Nothing is in force yet: touching the interface could remove one an in-flight operation needs.
		return
	}
	if changed, err := a.ensurePortalIface(ctx, vip); err != nil {
		a.logOnce("portal_iface", err.Error())
	} else {
		a.logOnce("portal_iface", "")
		if changed {
			slog.Warn("netd: the portal interface had drifted and was re-asserted", "iface", netcfg.PortalIface, "address", vip)
		}
	}
	tls := a.renderTopo().PortalTLS
	if changed, prev, err := a.writeUnboundPortal(vip, tls); err != nil {
		a.logOnce("unbound_portal", err.Error())
	} else if changed {
		if rerr := a.reloadUnboundPortal(ctx, prev); rerr != nil {
			a.logOnce("unbound_portal", rerr.Error())
		} else {
			a.logOnce("unbound_portal", "")
			slog.Info("netd: portal hostnames resolver fragment updated", "names", netcfg.PortalHostnames(tls), "address", vip)
		}
	}
	if _, err := a.syncKeaCaptiveLocked(ctx); err != nil {
		a.logOnce("kea_option114", err.Error())
	} else {
		a.logOnce("kea_option114", "")
	}
}

func (a *applier) portalRuntimeLoop(ctx context.Context) {
	a.portalRefresh(ctx)
	t := time.NewTicker(portalRefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.portalRefresh(ctx)
		}
	}
}

type portalIPReq struct {
	PortalIP string `json:"portal_ip"`
}

// portalIPValidate serves POST /v1/portal-ip/validate and changes nothing; 400 on an unreadable body, 500 when the check cannot be made (fail closed).
func (s *server) portalIPValidate(w http.ResponseWriter, r *http.Request) {
	var req portalIPReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "body must be {\"portal_ip\": \"<IPv4>\"}"})
		return
	}
	res, err := s.ap.ValidatePortalIP(r.Context(), req.PortalIP)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, res)
}
