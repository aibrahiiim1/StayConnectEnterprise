package main

// System (WAN/LAN) network management — the appliance's OWN base networking.
// This is distinct from the guest-VLAN overlay (apply.go/netcfg): here netd
// reads and safely changes ens160 (WAN/management) and the br-lan bridge via
// netplan, with backups, a confirm/auto-rollback window and a full audit trail.
//
// netd is the ONLY component that touches netplan; edged (unprivileged) proxies
// operator requests here over the unix socket. A single in-process mutex plus an
// on-disk lock file prevent concurrent system-network changes.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/sysnet"
)

const (
	wanNetplanFile = "/etc/netplan/01-static.yaml"
	lanNetplanFile = "/etc/netplan/02-lan-bridge.yaml"
	sysnetBackups  = "/root/backups/sysnet"
	sysnetLockFile = "/run/stayconnect/sysnet.lock"
	sysnetCommit   = "/run/stayconnect-net-committed"
	// Pending marker is PERSISTENT (survives a netd restart/crash) so netd can
	// reconcile an unconfirmed change on boot (carryover E). /run is tmpfs and
	// would be lost on reboot, hiding a still-applied-but-unconfirmed change.
	sysnetPendingFile = "/var/lib/stayconnect/sysnet-pending.json"
)

// pendingChange records an applied-but-unconfirmed system-network change so the
// in-process watchdog and boot reconciler can act on it and audit it.
type pendingChange struct {
	RevisionID string `json:"revision_id"`
	Deadline   int64  `json:"deadline"`
	Backup     string `json:"backup"`
	MgmtURL    string `json:"mgmt_url"`
	Actor      string `json:"actor"`
	ActorID    string `json:"actor_id"`
	SourceIP   string `json:"source_ip"`
	Target     string `json:"target"`
}

type sysNetMgr struct {
	mu            sync.Mutex
	st            *store
	wanIface      string
	lanPhys       string
	lanBr         string
	mgmtAddr      string
	confirmWindow time.Duration
	dryRun        bool
}

// ---- state model returned to the UI ----

type ifaceRuntime struct {
	Name    string   `json:"name"`
	MAC     string   `json:"mac"`
	LinkUp  bool     `json:"link_up"`
	Addrs   []string `json:"addrs"`
	Driver  string   `json:"driver,omitempty"`
	Master  string   `json:"master,omitempty"`
	Members []string `json:"members,omitempty"`
}

type connState struct {
	GatewayReachable bool `json:"gateway_reachable"`
	InternetOK       bool `json:"internet_ok"`
	DNSOK            bool `json:"dns_ok"`
}

type sysNetState struct {
	WAN struct {
		Interface         string    `json:"interface"`
		MAC               string    `json:"mac"`
		LinkUp            bool      `json:"link_up"`
		Mode              string    `json:"mode"`
		IP                string    `json:"ip"`
		PrefixLen         int       `json:"prefix_len"`
		Netmask           string    `json:"netmask"`
		Gateway           string    `json:"gateway"`
		DNS               []string  `json:"dns"`
		ManagementURL     string    `json:"management_url"`
		OutboundInterface string    `json:"outbound_interface"`
		Connectivity      connState `json:"connectivity"`
		PersistentIP      string    `json:"persistent_ip"`
		Drift             bool      `json:"drift"`
	} `json:"wan"`
	LAN struct {
		PhysicalInterface string   `json:"physical_interface"`
		Bridge            string   `json:"bridge"`
		MAC               string   `json:"mac"`
		LinkUp            bool     `json:"link_up"`
		IP                string   `json:"ip"`
		PrefixLen         int      `json:"prefix_len"`
		Netmask           string   `json:"netmask"`
		GatewayIP         string   `json:"gateway_ip"`
		DHCPEnabled       bool     `json:"dhcp_enabled"`
		DHCPStart         string   `json:"dhcp_start"`
		DHCPEnd           string   `json:"dhcp_end"`
		DHCPLeaseSeconds  int      `json:"dhcp_lease_seconds"`
		DNS               []string `json:"dns"`
		Members           []string `json:"members"`
	} `json:"lan"`
	Pending *pendingSysNet `json:"pending,omitempty"`
}

type pendingSysNet struct {
	DeadlineUnix  int64  `json:"deadline_unix"`
	ManagementURL string `json:"management_url"`
	BackupPath    string `json:"backup_path"`
}

// currentWANLAN returns the effective WAN/LAN config as sysnet types (for
// merge/validate/render), read from the live runtime + DB.
func (m *sysNetMgr) currentWANLAN(ctx context.Context) (sysnet.WANConfig, sysnet.LANConfig) {
	w := sysnet.WANConfig{Interface: m.wanIface, Mode: "static"}
	ip, plen := primaryV4(m.wanIface)
	w.IP, w.PrefixLen = ip, plen
	w.Gateway = defaultGatewayFor(m.wanIface)
	w.DNS = ifaceDNS(m.wanIface)

	l := sysnet.LANConfig{PhysicalInterface: m.lanPhys, Bridge: m.lanBr}
	lip, lplen := primaryV4(m.lanBr)
	l.IP, l.PrefixLen = lip, lplen
	// DHCP config from the site DB (netd's source of truth for br-lan).
	if m.st != nil {
		m.st.db.QueryRow(ctx, `
            SELECT COALESCE(host(gateway_ip),''), COALESCE(lease_default_seconds,3600),
                   (dhcp_mode='local')
              FROM guest_networks WHERE bridge_name=$1 LIMIT 1`, m.lanBr).
			Scan(&l.IP, &l.DHCPLeaseSeconds, &l.DHCPEnabled)
		if l.IP == "" {
			l.IP = lip
		}
		var start, end string
		m.st.db.QueryRow(ctx, `
            SELECT COALESCE(host(start_ip),''), COALESCE(host(end_ip),'')
              FROM dhcp_pools p JOIN guest_networks g ON g.id=p.guest_network_id
             WHERE g.bridge_name=$1 LIMIT 1`, m.lanBr).Scan(&start, &end)
		l.DHCPStart, l.DHCPEnd = start, end
	}
	l.DNS = []string{l.IP} // appliance (unbound) is the guest resolver
	return w, l
}

func (m *sysNetMgr) readState(ctx context.Context) sysNetState {
	var s sysNetState
	w, l := m.currentWANLAN(ctx)

	wr := ifaceInfo(m.wanIface)
	s.WAN.Interface = m.wanIface
	s.WAN.MAC = wr.MAC
	s.WAN.LinkUp = wr.LinkUp
	s.WAN.Mode = w.Mode
	s.WAN.IP = w.IP
	s.WAN.PrefixLen = w.PrefixLen
	s.WAN.Netmask = maskString(w.PrefixLen)
	s.WAN.Gateway = w.Gateway
	s.WAN.DNS = w.DNS
	s.WAN.ManagementURL = sysnet.ManagementURL(w)
	s.WAN.OutboundInterface = outboundIface()
	s.WAN.Connectivity = m.connectivity(w)
	s.WAN.PersistentIP = persistentWANIP(m.wanIface)
	s.WAN.Drift = s.WAN.PersistentIP != "" && s.WAN.PersistentIP != w.IP

	lr := ifaceInfo(m.lanBr)
	s.LAN.PhysicalInterface = m.lanPhys
	s.LAN.Bridge = m.lanBr
	s.LAN.MAC = lr.MAC
	s.LAN.LinkUp = lr.LinkUp
	s.LAN.IP = l.IP
	s.LAN.PrefixLen = l.PrefixLen
	s.LAN.Netmask = maskString(l.PrefixLen)
	s.LAN.GatewayIP = l.IP
	s.LAN.DHCPEnabled = l.DHCPEnabled
	s.LAN.DHCPStart = l.DHCPStart
	s.LAN.DHCPEnd = l.DHCPEnd
	s.LAN.DHCPLeaseSeconds = l.DHCPLeaseSeconds
	s.LAN.DNS = l.DNS
	s.LAN.Members = bridgeMembers(m.lanBr)

	if pc, ok := readPendingChange(); ok {
		s.Pending = &pendingSysNet{DeadlineUnix: pc.Deadline, ManagementURL: pc.MgmtURL, BackupPath: pc.Backup}
	}
	return s
}

func (m *sysNetMgr) connectivity(w sysnet.WANConfig) connState {
	var c connState
	if w.Gateway != "" {
		c.GatewayReachable = ping(w.Gateway)
	}
	c.InternetOK = ping("8.8.8.8")
	c.DNSOK = resolves("github.com")
	return c
}

// ---- validate ----

func (m *sysNetMgr) validate(ctx context.Context, p sysnet.Proposal) (sysnet.ValidationResult, sysnet.WANConfig, sysnet.LANConfig) {
	cw, cl := m.currentWANLAN(ctx)
	w, l := sysnet.Merge(cw, cl, p)
	return sysnet.ValidateFull(w, l), w, l
}

// ---- apply / confirm / rollback ----

type applySysResult struct {
	OK            bool                    `json:"ok"`
	State         string                  `json:"state"` // pending_confirmation | failed | rolled_back
	Validation    sysnet.ValidationResult `json:"validation"`
	ManagementURL string                  `json:"management_url"`
	BackupPath    string                  `json:"backup_path,omitempty"`
	DeadlineUnix  int64                   `json:"deadline_unix,omitempty"`
	Message       string                  `json:"message,omitempty"`
	Verify        map[string]bool         `json:"verify,omitempty"`
}

func (m *sysNetMgr) apply(ctx context.Context, p sysnet.Proposal, actor, actorID, srcIP string) (*applySysResult, error) {
	if !m.mu.TryLock() {
		return &applySysResult{OK: false, State: "failed", Message: "another network change is in progress"}, nil
	}
	defer m.mu.Unlock()

	res, w, l := m.validate(ctx, p)
	out := &applySysResult{Validation: res, ManagementURL: sysnet.ManagementURL(w)}
	target := changeTarget(p)
	revID := genUUID()
	pc := pendingChange{RevisionID: revID, Actor: actor, ActorID: actorID, SourceIP: srcIP, Target: target, MgmtURL: sysnet.ManagementURL(w)}
	m.auditEvent(ctx, revID, "network.apply.requested", pc, "", "")
	if !res.OK {
		out.State = "failed"
		out.Message = "validation failed"
		m.auditEvent(ctx, revID, "network.apply.rejected", pc, "validation_failed", "")
		return out, nil
	}

	if m.dryRun {
		out.OK = true
		out.State = "pending_confirmation"
		out.Message = "dry-run: not applied"
		return out, nil
	}

	// 1. backup
	stamp := time.Now().UTC().Format("20060102-150405")
	bk := filepath.Join(sysnetBackups, stamp)
	if err := os.MkdirAll(bk, 0o700); err != nil {
		return nil, err
	}
	_ = copyFile(wanNetplanFile, filepath.Join(bk, "01-static.yaml"))
	_ = copyFile(lanNetplanFile, filepath.Join(bk, "02-lan-bridge.yaml"))
	out.BackupPath = bk
	pc.Backup = bk

	// 2. render + write netplan (structured serializer)
	if p.WAN != nil {
		if err := os.WriteFile(wanNetplanFile, []byte(sysnet.RenderWANNetplan(w)), 0o600); err != nil {
			m.restore(ctx, bk)
			return nil, err
		}
	}
	if p.LAN != nil {
		if err := os.WriteFile(lanNetplanFile, []byte(sysnet.RenderLANNetplan(l)), 0o600); err != nil {
			m.restore(ctx, bk)
			return nil, err
		}
	}

	// 3. validate the generated config
	if err := run("netplan", "generate"); err != nil {
		m.restore(ctx, bk)
		out.State = "failed"
		out.Message = "netplan generate failed: " + err.Error()
		m.auditEvent(ctx, revID, "network.apply.rejected", pc, "generate_failed: "+err.Error(), bk)
		return out, nil
	}

	// 4. begin apply — persist the pending marker + reset the commit flag. The
	// in-process watchdog (sysnetWatchdog) auto-rolls-back if unconfirmed.
	os.Remove(sysnetCommit)
	deadline := time.Now().Add(m.confirmWindow)
	pc.Deadline = deadline.Unix()
	m.auditEvent(ctx, revID, "network.apply.started", pc, "", bk)

	// 5. apply
	if err := run("netplan", "apply"); err != nil {
		m.restore(ctx, bk)
		clearPending()
		out.State = "failed"
		out.Message = "netplan apply failed: " + err.Error()
		m.auditEvent(ctx, revID, "network.apply.failed", pc, "netplan_apply: "+err.Error(), bk)
		return out, nil
	}
	time.Sleep(3 * time.Second)

	// 6. verify — any critical failure => immediate in-process rollback
	v := m.verify(w, l)
	out.Verify = v
	critical := v["wan_ip"] && v["gateway"] && v["management_local"]
	if !critical {
		m.auditEvent(ctx, revID, "network.apply.verify_failed", pc, fmt.Sprintf("%v", v), bk)
		m.auditEvent(ctx, revID, "network.rollback.automatic.started", pc, "verify_failed", bk)
		m.restore(ctx, bk)
		clearPending()
		m.auditEvent(ctx, revID, "network.rollback.automatic.succeeded", pc, "verify_failed", bk)
		out.State = "rolled_back"
		out.Message = "post-apply verification failed; restored last-known-good"
		return out, nil
	}

	// 7. enter pending_confirmation — persist the marker so the watchdog (and a
	// boot reconcile after a crash) can auto-roll-back if unconfirmed.
	m.writePendingLocked(pc)
	out.OK = true
	out.State = "pending_confirmation"
	out.DeadlineUnix = deadline.Unix()
	out.Message = fmt.Sprintf("applied; confirm within %s from the new management URL or it auto-rolls-back", m.confirmWindow)
	m.auditEvent(ctx, revID, "network.apply.pending_confirmation", pc, "", bk)
	return out, nil
}

// writePendingLocked persists the marker (helper kept for symmetry/testing).
func (m *sysNetMgr) writePendingLocked(pc pendingChange) { writePendingChange(pc) }

func (m *sysNetMgr) confirm(ctx context.Context, actor, actorID, srcIP string) error {
	pc, ok := readPendingChange()
	if !ok {
		return fmt.Errorf("no pending network change to confirm")
	}
	// commit: flag prevents any concurrent rollback; clear the pending marker.
	_ = os.WriteFile(sysnetCommit, []byte(time.Now().String()), 0o644)
	pc.Actor, pc.ActorID, pc.SourceIP = actor, actorID, srcIP
	clearPending()
	m.auditEvent(ctx, pc.RevisionID, "network.apply.confirmed", pc, "", pc.Backup)
	return nil
}

func (m *sysNetMgr) rollback(ctx context.Context, actor, actorID, srcIP string) error {
	pc, ok := readPendingChange()
	bk := pc.Backup
	if !ok || bk == "" {
		bk = latestBackup()
	}
	if bk == "" {
		return fmt.Errorf("no backup to roll back to")
	}
	pc.Actor, pc.ActorID, pc.SourceIP = actor, actorID, srcIP
	m.auditEvent(ctx, pc.RevisionID, "network.rollback.manual.started", pc, "operator requested", bk)
	m.restore(ctx, bk)
	clearPending()
	m.auditEvent(ctx, pc.RevisionID, "network.rollback.manual.succeeded", pc, "operator requested", bk)
	return nil
}

// sysnetWatchdog auto-rolls-back an applied-but-unconfirmed change once its
// confirmation deadline passes (carryover D — in-process, fully audited). It
// replaces the previous detached systemd-run script, which could not write
// structured audit rows.
func (m *sysNetMgr) sysnetWatchdog(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.maybeAutoRollback(ctx)
		}
	}
}

func (m *sysNetMgr) maybeAutoRollback(ctx context.Context) {
	pc, ok := readPendingChange()
	if !ok {
		return
	}
	if _, err := os.Stat(sysnetCommit); err == nil {
		return // committed
	}
	if time.Now().Unix() < pc.Deadline {
		return // still within window
	}
	if m.dryRun || !m.mu.TryLock() {
		return
	}
	defer m.mu.Unlock()
	// re-check under lock (a confirm may have raced)
	if pc2, ok := readPendingChange(); !ok || pc2.RevisionID != pc.RevisionID {
		return
	}
	m.auditEvent(ctx, pc.RevisionID, "network.apply.confirmation_timeout", pc, fmt.Sprintf("no confirmation within %s", m.confirmWindow), pc.Backup)
	m.auditEvent(ctx, pc.RevisionID, "network.rollback.automatic.started", pc, "confirmation_timeout", pc.Backup)
	if pc.Backup == "" {
		clearPending()
		m.auditEvent(ctx, pc.RevisionID, "network.rollback.automatic.failed", pc, "no backup recorded", "")
		return
	}
	m.restore(ctx, pc.Backup)
	clearPending()
	m.auditEvent(ctx, pc.RevisionID, "network.rollback.automatic.succeeded", pc, "confirmation_timeout", pc.Backup)
}

// ReconcileStalePendingOnBoot inspects a persistent pending marker on netd
// startup (carryover E). If a change was applied but never confirmed (e.g. netd
// crashed or the box rebooted during the window), it is safely rolled back and
// the reconciliation is audited. A marker still within its window is left for
// the watchdog. Committed/absent markers are cleaned up.
func (m *sysNetMgr) ReconcileStalePendingOnBoot(ctx context.Context) {
	pc, ok := readPendingChange()
	if !ok {
		return
	}
	if _, err := os.Stat(sysnetCommit); err == nil {
		clearPending() // was confirmed; just tidy the marker
		return
	}
	if time.Now().Unix() < pc.Deadline {
		m.auditEvent(ctx, pc.RevisionID, "network.reconcile.pending_within_window", pc, "watchdog will handle", pc.Backup)
		return
	}
	m.auditEvent(ctx, pc.RevisionID, "network.reconcile.stale_pending", pc, "unconfirmed across restart", pc.Backup)
	m.auditEvent(ctx, pc.RevisionID, "network.rollback.automatic.started", pc, "boot_reconcile", pc.Backup)
	if pc.Backup == "" {
		clearPending()
		m.auditEvent(ctx, pc.RevisionID, "network.rollback.automatic.failed", pc, "no backup recorded", "")
		return
	}
	if !m.dryRun {
		m.restore(ctx, pc.Backup)
	}
	clearPending()
	m.auditEvent(ctx, pc.RevisionID, "network.rollback.automatic.succeeded", pc, "boot_reconcile", pc.Backup)
}

func (m *sysNetMgr) restore(ctx context.Context, bk string) {
	_ = copyFile(filepath.Join(bk, "01-static.yaml"), wanNetplanFile)
	_ = copyFile(filepath.Join(bk, "02-lan-bridge.yaml"), lanNetplanFile)
	if !m.dryRun {
		_ = run("netplan", "apply")
	}
}

func (m *sysNetMgr) verify(w sysnet.WANConfig, l sysnet.LANConfig) map[string]bool {
	v := map[string]bool{}
	v["wan_ip"] = w.IP == "" || ifaceHasIP(w.Interface, w.IP)
	v["gateway"] = w.Gateway == "" || ping(w.Gateway)
	v["internet"] = ping("8.8.8.8")
	v["dns"] = resolves("github.com")
	v["lan_bridge"] = ifaceHasIP(l.Bridge, l.IP)
	v["lan_member"] = ifaceMaster(l.PhysicalInterface) == l.Bridge
	v["management_local"] = tcpProbe("127.0.0.1:3100") || tcpProbe(w.IP+":443")
	return v
}

// ---- audit ----

func (m *sysNetMgr) audit(ctx context.Context, actor, actorID, srcIP, action, target string, prev any, val sysnet.ValidationResult, apply, confirm, rollback, failure, bk string) {
	if m.st == nil {
		return
	}
	pv, _ := json.Marshal(prev)
	vv, _ := json.Marshal(val)
	_, _ = m.st.db.Exec(ctx, `
        INSERT INTO system_network_audit
          (actor, actor_id, source_ip, action, target, requested_config, validation_result,
           apply_result, confirm_result, rollback_result, failure_reason, backup_path)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		actor, uuidOrNil(actorID), srcIP, action, target, pv, vv,
		nullStr(apply), nullStr(confirm), nullStr(rollback), nullStr(failure), nullStr(bk))
}

func (m *sysNetMgr) history(ctx context.Context, limit int) []map[string]any {
	out := []map[string]any{}
	if m.st == nil {
		return out
	}
	rows, err := m.st.db.Query(ctx, `
        SELECT to_char(created_at,'YYYY-MM-DD"T"HH24:MI:SSOF'), actor, source_ip, action, target,
               COALESCE(apply_result,''), COALESCE(confirm_result,''), COALESCE(rollback_result,''),
               COALESCE(failure_reason,''), COALESCE(backup_path,'')
          FROM system_network_audit ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var at, actor, ip, action, target, ap, cf, rb, fr, bk string
		if rows.Scan(&at, &actor, &ip, &action, &target, &ap, &cf, &rb, &fr, &bk) == nil {
			out = append(out, map[string]any{
				"at": at, "actor": actor, "source_ip": ip, "action": action, "target": target,
				"apply_result": ap, "confirm_result": cf, "rollback_result": rb,
				"failure_reason": fr, "backup_path": bk,
			})
		}
	}
	return out
}

// ---- diagnostics (read-only, secrets sanitized) ----

func (m *sysNetMgr) diagnostics() map[string]string {
	d := map[string]string{}
	d["ip_addr"] = cmdOut("ip", "-br", "addr")
	d["ip_route"] = cmdOut("ip", "route")
	d["ip_rule"] = cmdOut("ip", "rule")
	d["bridge_link"] = cmdOut("bridge", "link")
	d["resolvectl"] = sanitize(cmdOut("resolvectl", "status"))
	d["networkctl"] = cmdOut("networkctl", "status", "--no-pager")
	d["netplan_get"] = cmdOut("netplan", "get")
	return d
}

// ---- HTTP handlers ----

func (s *server) sysnetGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.sn.readState(r.Context()))
}

func (s *server) sysnetValidate(w http.ResponseWriter, r *http.Request) {
	var p sysnet.Proposal
	_ = json.NewDecoder(r.Body).Decode(&p)
	res, wc, lc := s.sn.validate(r.Context(), p)
	writeJSON(w, 200, map[string]any{"validation": res, "management_url": sysnet.ManagementURL(wc), "effective": map[string]any{"wan": wc, "lan": lc}})
}

func (s *server) sysnetApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Proposal sysnet.Proposal `json:"proposal"`
		Actor    string          `json:"actor"`
		ActorID  string          `json:"actor_id"`
		SourceIP string          `json:"source_ip"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	res, err := s.sn.apply(r.Context(), req.Proposal, req.Actor, req.ActorID, req.SourceIP)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, res)
}

func (s *server) sysnetConfirm(w http.ResponseWriter, r *http.Request) {
	var req struct{ Actor, ActorID, SourceIP string }
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := s.sn.confirm(r.Context(), req.Actor, req.ActorID, req.SourceIP); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"state": "confirmed"})
}

func (s *server) sysnetRollback(w http.ResponseWriter, r *http.Request) {
	var req struct{ Actor, ActorID, SourceIP string }
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := s.sn.rollback(r.Context(), req.Actor, req.ActorID, req.SourceIP); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"state": "rolled_back"})
}

func (s *server) sysnetHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"history": s.sn.history(r.Context(), 50)})
}

func (s *server) sysnetDiagnostics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"diagnostics": s.sn.diagnostics()})
}

// ---- low-level helpers (runtime reads / exec) ----

func changeTarget(p sysnet.Proposal) string {
	switch {
	case p.WAN != nil && p.LAN != nil:
		return "both"
	case p.LAN != nil:
		return "lan"
	default:
		return "wan"
	}
}

func cfgJSON(w sysnet.WANConfig, l sysnet.LANConfig) map[string]any {
	return map[string]any{"wan": w, "lan": l}
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func run(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func cmdOut(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out))
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}

type ipAddrJSON struct {
	IfName    string `json:"ifname"`
	Address   string `json:"address"`
	OperState string `json:"operstate"`
	Master    string `json:"master"`
	AddrInfo  []struct {
		Family    string `json:"family"`
		Local     string `json:"local"`
		PrefixLen int    `json:"prefixlen"`
	} `json:"addr_info"`
}

func ipJSON(iface string) *ipAddrJSON {
	out := cmdOut("ip", "-j", "addr", "show", iface)
	var arr []ipAddrJSON
	if json.Unmarshal([]byte(out), &arr) != nil || len(arr) == 0 {
		return nil
	}
	return &arr[0]
}

func ifaceInfo(iface string) ifaceRuntime {
	r := ifaceRuntime{Name: iface}
	j := ipJSON(iface)
	if j == nil {
		return r
	}
	r.MAC = j.Address
	r.LinkUp = strings.EqualFold(j.OperState, "UP") || strings.EqualFold(j.OperState, "UNKNOWN")
	for _, a := range j.AddrInfo {
		if a.Family == "inet" {
			r.Addrs = append(r.Addrs, fmt.Sprintf("%s/%d", a.Local, a.PrefixLen))
		}
	}
	return r
}

func primaryV4(iface string) (string, int) {
	j := ipJSON(iface)
	if j == nil {
		return "", 0
	}
	for _, a := range j.AddrInfo {
		if a.Family == "inet" {
			return a.Local, a.PrefixLen
		}
	}
	return "", 0
}

func defaultGatewayFor(iface string) string {
	out := cmdOut("ip", "route", "show", "default", "dev", iface)
	f := strings.Fields(out)
	for i, t := range f {
		if t == "via" && i+1 < len(f) {
			return f[i+1]
		}
	}
	return ""
}

func outboundIface() string {
	out := cmdOut("ip", "route", "get", "8.8.8.8")
	f := strings.Fields(out)
	for i, t := range f {
		if t == "dev" && i+1 < len(f) {
			return f[i+1]
		}
	}
	return ""
}

func ifaceDNS(iface string) []string {
	out := cmdOut("resolvectl", "dns", iface)
	// "Link 2 (ens160): 1.1.1.1 8.8.8.8"
	if i := strings.Index(out, ":"); i >= 0 {
		out = out[i+1:]
	}
	var dns []string
	for _, t := range strings.Fields(out) {
		if net.ParseIP(t) != nil {
			dns = append(dns, t)
		}
	}
	return dns
}

func persistentWANIP(iface string) string {
	// Ask netplan for the merged persistent intent.
	out := cmdOut("netplan", "get", "ethernets."+iface+".addresses")
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.Trim(strings.TrimPrefix(strings.TrimSpace(line), "-"), " \""))
		if ip, _, err := net.ParseCIDR(line); err == nil {
			return ip.String()
		}
	}
	return ""
}

func bridgeMembers(br string) []string {
	entries, _ := os.ReadDir("/sys/class/net/" + br + "/brif")
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func ifaceMaster(iface string) string {
	b, err := os.Readlink("/sys/class/net/" + iface + "/master")
	if err != nil {
		return ""
	}
	return filepath.Base(b)
}

func maskString(prefix int) string {
	if prefix < 0 || prefix > 32 {
		return ""
	}
	m := net.CIDRMask(prefix, 32)
	return fmt.Sprintf("%d.%d.%d.%d", m[0], m[1], m[2], m[3])
}

func ping(ip string) bool {
	return run("ping", "-c1", "-W2", "-n", ip) == nil
}

func resolves(host string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	return err == nil && len(addrs) > 0
}

func tcpProbe(hostport string) bool {
	c, err := net.DialTimeout("tcp", hostport, 2*time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func sanitize(s string) string {
	// no secrets in resolvectl, but keep the hook for future diagnostics.
	return s
}

// --- persistent pending marker (survives netd restart for boot reconcile) ---

func writePendingChange(pc pendingChange) {
	_ = os.MkdirAll(filepath.Dir(sysnetPendingFile), 0o700)
	b, _ := json.Marshal(pc)
	_ = os.WriteFile(sysnetPendingFile, b, 0o600)
}
func readPendingChange() (pendingChange, bool) {
	var pc pendingChange
	b, err := os.ReadFile(sysnetPendingFile)
	if err != nil {
		return pc, false
	}
	if json.Unmarshal(b, &pc) != nil {
		return pc, false
	}
	return pc, true
}

// clearPending removes BOTH the persistent pending marker and the commit flag —
// called on every terminal outcome (confirm / manual rollback / auto rollback /
// failed rollback / boot reconcile) so no stale marker is ever left behind
// (carryover E).
func clearPending() {
	_ = os.Remove(sysnetPendingFile)
	_ = os.Remove(sysnetCommit)
}

// genUUID returns a random RFC-4122 v4 UUID (netd has no google/uuid dep).
func genUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// auditEvent writes one structured row of the system-network change lifecycle.
// action is a network.* event name; rows sharing revisionID form the sequence.
func (m *sysNetMgr) auditEvent(ctx context.Context, revID, action string, pc pendingChange, reason, backup string) {
	if m.st == nil {
		return
	}
	_, _ = m.st.db.Exec(ctx, `
        INSERT INTO system_network_audit
          (revision_id, actor, actor_id, source_ip, action, target, reason, backup_path, deadline)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8, CASE WHEN $9=0 THEN NULL ELSE to_timestamp($9) END)`,
		nullStr(revID), pc.Actor, uuidOrNil(pc.ActorID), nullStr(pc.SourceIP),
		action, nullOr(pc.Target, "both"), nullStr(reason), nullStr(backup), pc.Deadline)
}

func nullOr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func latestBackup() string {
	entries, _ := os.ReadDir(sysnetBackups)
	var newest string
	for _, e := range entries {
		if e.IsDir() && e.Name() > newest {
			newest = e.Name()
		}
	}
	if newest == "" {
		return ""
	}
	return filepath.Join(sysnetBackups, newest)
}
