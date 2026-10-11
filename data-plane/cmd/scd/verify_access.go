package main

// TEMPORARY VERIFICATION ACCESS (contract §6.6; Product-Owner decision 2026-10-11, PR #211).
//
// A client who asks for an email code has to read it in a mail app before they are online. Opening mail
// services by host name cannot be made safe (a mail service's addresses serve the provider's other services
// too) and cannot cover every provider. So an issued email code earns the requesting DEVICE a short window of
// general internet access, bounded and metered by the kernel:
//
//	duration       a timed admission element; the kernel ends it on time even if scd is down
//	data           one quota object counting every byte, both directions; past the cap every packet drops
//	rate           two limit objects (up, down) policing the device to the configured speeds
//
// The window starts only after the code is issued AND sent, and ends at the first of: verification (or any
// other successful sign-in from the device), expiry, data exhaustion, cancellation ("use a different
// address"). It is not an entitlement -- nothing here writes a purchase, an entitlement, a quota or a session,
// so a client's existing access is untouched.
//
// WHAT STOPS IT BEING A FREE-INTERNET MACHINE. One live window per device (a second request reuses it, never
// resets it -- the counted bytes and the deadline carry on); at most N windows per device and N per destination
// address in a rolling 24 hours; at most M live windows on the site at once. These counts live in
// iam_v2.verification_access_grants, so they survive restarts. A device that changes its hardware address is
// a new device to the network -- that cannot be prevented, only bounded: it still needs a fresh destination
// address within the per-destination limit, a sendable code, and a free slot under the site cap.
//
// RESTART-SAFE. The table is the authority; reconcileVerifyAccess (every 2 s) re-installs a live grant whose
// kernel state is missing (a netd re-render, an scd or appliance restart) with what is LEFT of its window and
// the bytes already counted, ends what is due, and removes strays.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/nft"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
)

const verifyDestDomain = "onegate-verify-dest:v1"

// verifyAccessView is what the portal is told about a window: how long, how much.
type verifyAccessView struct {
	Seconds  int `json:"seconds"`
	DataMB   int `json:"data_mb"`
	DownKbps int `json:"down_kbps"`
	UpKbps   int `json:"up_kbps"`
}

// verifyKernel is the kernel surface the grant logic needs (nft.Client in production; a fake in tests).
type verifyKernel interface {
	VerifyApply(ctx context.Context, g nft.VerifyGrant) error
	VerifyRemove(ctx context.Context, key, bridge string, ip net.IP)
	VerifyRead(ctx context.Context) (nft.VerifyState, error)
}

func (s *server) verifyKernel() verifyKernel {
	if s.verifyK != nil {
		return s.verifyK
	}
	if s.nft != nil && s.nft.client != nil {
		return s.nft.client
	}
	return nil
}

// temporaryAccessSettings is the site's window as enforced; Enabled is false when Email code itself is off.
func (s *server) temporaryAccessSettings(ctx context.Context) tenantcfg.TemporaryAccessEffective {
	var cfg *tenantcfg.AuthMethods
	var err error
	if s.methodSwitches != nil {
		cfg, err = s.methodSwitches(ctx)
	} else if s.db != nil {
		cfg, err = tenantcfg.Load(ctx, s.db, s.tenID)
	}
	if err != nil || cfg == nil || cfg.Email == nil || !cfg.Email.Enabled {
		return tenantcfg.TemporaryAccessEffective{}
	}
	return cfg.Email.TemporaryAccess.Effective()
}

// verifyDestDigest keys the destination under the appliance-local key, so the ledger can count windows per
// address without holding the address. No key, no digest, no window.
func (s *server) verifyDestDigest(dest string) string {
	if len(s.resumeKey) < 32 {
		return ""
	}
	h := hmac.New(sha256.New, s.resumeKey)
	h.Write([]byte(verifyDestDomain + "\x00" + strings.ToLower(strings.TrimSpace(dest))))
	return hex.EncodeToString(h.Sum(nil))
}

func grantKey(id string) string {
	k := strings.ReplaceAll(id, "-", "")
	if len(k) < 12 {
		return ""
	}
	return k[:12]
}

// grantVerifyAccess opens (or reports the already-open) window for the device that was just sent a code.
// false = no window; the code is unaffected either way.
func (s *server) grantVerifyAccess(ctx context.Context, mac string, ip net.IP, challengeID, dest string) (verifyAccessView, bool) {
	k := s.verifyKernel()
	if s.db == nil || k == nil || ip == nil || ip.To4() == nil || mac == "" {
		return verifyAccessView{}, false
	}
	set := s.temporaryAccessSettings(ctx)
	if !set.Enabled {
		return verifyAccessView{}, false
	}
	digest := s.verifyDestDigest(dest)
	if digest == "" {
		slog.Warn("temporary access unavailable: anonymous-access key missing (run keybootstrap)")
		return verifyAccessView{}, false
	}
	nc := s.resolveNetwork(ctx, ip)
	if nc.NetworkID == "" || nc.Bridge == "" {
		return verifyAccessView{}, false
	}
	type grantRow struct {
		id, bridge string
		expires    time.Time
		capBytes   int64
		used       int64
		up, down   int
		fresh      bool
	}
	var g grantRow
	refused := ""
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('verify_access'), hashtext($1))`, mac); err != nil {
			return err
		}
		// ONE LIVE WINDOW PER DEVICE: reuse it, never reset it.
		err := tx.QueryRow(ctx, `SELECT id::text, bridge, expires_at, data_cap_bytes, used_bytes, up_kbps, down_kbps
		     FROM iam_v2.verification_access_grants
		    WHERE tenant_id=$1 AND site_id=$2 AND device_mac=$3::macaddr AND ended_at IS NULL`,
			s.tenID, s.siteID, mac).Scan(&g.id, &g.bridge, &g.expires, &g.capBytes, &g.used, &g.up, &g.down)
		if err == nil {
			if g.expires.After(time.Now()) && g.used < g.capBytes {
				return nil
			}
			reason := "EXPIRED"
			if g.used >= g.capBytes {
				reason = "DATA_EXHAUSTED"
			}
			if _, err := tx.Exec(ctx, `UPDATE iam_v2.verification_access_grants SET ended_at=now(), end_reason=$2 WHERE id=$1::uuid`, g.id, reason); err != nil {
				return err
			}
			g = grantRow{}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var perDevice, perDest, live int
		if err := tx.QueryRow(ctx, `SELECT
		      count(*) FILTER (WHERE device_mac=$3::macaddr AND started_at > now() - interval '24 hours'),
		      count(*) FILTER (WHERE destination_digest=$4 AND started_at > now() - interval '24 hours'),
		      count(*) FILTER (WHERE ended_at IS NULL AND expires_at > now())
		    FROM iam_v2.verification_access_grants WHERE tenant_id=$1 AND site_id=$2`,
			s.tenID, s.siteID, mac, digest).Scan(&perDevice, &perDest, &live); err != nil {
			return err
		}
		switch {
		case perDevice >= set.PerDeviceDaily:
			refused = "device_daily_limit"
		case perDest >= set.PerDestinationDaily:
			refused = "destination_daily_limit"
		case live >= set.MaxConcurrent:
			refused = "site_concurrent_limit"
		}
		if refused != "" {
			return nil
		}
		g = grantRow{bridge: nc.Bridge, capBytes: int64(set.DataMB) * 1024 * 1024, up: set.UpKbps, down: set.DownKbps, fresh: true}
		var chal any
		if challengeID != "" {
			chal = challengeID
		}
		var gnet any
		if nc.NetworkID != "" {
			gnet = nc.NetworkID
		}
		return tx.QueryRow(ctx, `INSERT INTO iam_v2.verification_access_grants
		      (tenant_id, site_id, guest_network_id, device_mac, ip, bridge, challenge_id, destination_digest,
		       expires_at, data_cap_bytes, down_kbps, up_kbps)
		    VALUES ($1,$2,$3::uuid,$4::macaddr,$5::inet,$6,$7::uuid,$8, now() + make_interval(mins => $9), $10,$11,$12)
		    RETURNING id::text, expires_at`,
			s.tenID, s.siteID, gnet, mac, ip.String(), nc.Bridge, chal, digest, set.Minutes, g.capBytes, g.down, g.up).
			Scan(&g.id, &g.expires)
	})
	if err != nil {
		slog.Warn("temporary access: grant failed", "err", err)
		return verifyAccessView{}, false
	}
	if refused != "" {
		slog.Info("temporary access refused", "reason", refused)
		return verifyAccessView{}, false
	}
	if g.fresh {
		kg := nft.VerifyGrant{Key: grantKey(g.id), Bridge: g.bridge, IP: ip, Remaining: time.Until(g.expires),
			CapBytes: g.capBytes, UsedBytes: 0, UpKbps: g.up, DownKbps: g.down}
		if err := k.VerifyApply(ctx, kg); err != nil {
			slog.Warn("temporary access: kernel install failed", "err", err)
			_, _ = s.db.Exec(ctx, `UPDATE iam_v2.verification_access_grants SET ended_at=now(), end_reason='RESTORE_FAILED' WHERE id=$1::uuid AND ended_at IS NULL`, g.id)
			k.VerifyRemove(ctx, kg.Key, kg.Bridge, ip)
			return verifyAccessView{}, false
		}
		slog.Info("temporary access opened", "minutes", set.Minutes, "data_mb", set.DataMB)
	}
	left := int(time.Until(g.expires).Seconds())
	if left < 0 {
		left = 0
	}
	return verifyAccessView{Seconds: left, DataMB: int((g.capBytes - g.used) / (1024 * 1024)), DownKbps: g.down, UpKbps: g.up}, true
}

// endVerifyAccess ends the device's live window, if it has one, recording the bytes the kernel counted.
func (s *server) endVerifyAccess(ctx context.Context, mac, reason string) {
	k := s.verifyKernel()
	if s.db == nil || mac == "" {
		return
	}
	hw, err := net.ParseMAC(strings.TrimSpace(mac))
	if err != nil {
		return
	}
	var id, bridge, ip string
	if err := s.db.QueryRow(ctx, `SELECT id::text, bridge, host(ip) FROM iam_v2.verification_access_grants
	     WHERE tenant_id=$1 AND site_id=$2 AND device_mac=$3::macaddr AND ended_at IS NULL`,
		s.tenID, s.siteID, hw.String()).Scan(&id, &bridge, &ip); err != nil {
		return
	}
	used := int64(-1)
	if k != nil {
		if st, err := k.VerifyRead(ctx); err == nil {
			if u, ok := st.Used[grantKey(id)]; ok {
				used = u
			}
		}
	}
	_, _ = s.db.Exec(ctx, `UPDATE iam_v2.verification_access_grants
	     SET ended_at=now(), end_reason=$2, used_bytes=GREATEST(used_bytes, $3) WHERE id=$1::uuid AND ended_at IS NULL`, id, reason, used)
	if k != nil {
		k.VerifyRemove(ctx, grantKey(id), bridge, net.ParseIP(ip))
	}
	slog.Info("temporary access ended", "reason", reason)
}

// reconcileVerifyAccess brings the kernel and the ledger together. It is the only path that re-installs a
// window, and it never extends one: a re-install carries the remaining time and the counted bytes.
func (s *server) reconcileVerifyAccess(ctx context.Context) {
	k := s.verifyKernel()
	if s.db == nil || k == nil {
		return
	}
	st, err := k.VerifyRead(ctx)
	if err != nil {
		return // kernel state unknown: change nothing
	}
	type row struct {
		id, bridge, ip string
		expires        time.Time
		capBytes, used int64
		up, down       int
		signedIn       bool
	}
	rows, err := s.db.Query(ctx, `SELECT g.id::text, g.bridge, host(g.ip), g.expires_at, g.data_cap_bytes, g.used_bytes, g.up_kbps, g.down_kbps,
	       EXISTS (SELECT 1 FROM iam_v2.sessions se WHERE se.tenant_id=g.tenant_id AND se.site_id=g.site_id
	                  AND se.mac=g.device_mac AND se.state='active')
	  FROM iam_v2.verification_access_grants g
	 WHERE g.tenant_id=$1 AND g.site_id=$2 AND g.ended_at IS NULL`, s.tenID, s.siteID)
	if err != nil {
		return
	}
	var live []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.bridge, &r.ip, &r.expires, &r.capBytes, &r.used, &r.up, &r.down, &r.signedIn) == nil {
			live = append(live, r)
		}
	}
	rows.Close()
	keep := map[string]bool{}
	admittedKeep := map[string]bool{}
	now := time.Now()
	for _, r := range live {
		key := grantKey(r.id)
		used := r.used
		if u, ok := st.Used[key]; ok && u > used {
			used = u
		}
		end := ""
		switch {
		case r.signedIn:
			end = "SIGNED_IN"
		case !now.Before(r.expires):
			end = "EXPIRED"
		case used >= r.capBytes:
			end = "DATA_EXHAUSTED"
		}
		if end != "" {
			_, _ = s.db.Exec(ctx, `UPDATE iam_v2.verification_access_grants SET ended_at=now(), end_reason=$2, used_bytes=GREATEST(used_bytes,$3)
			     WHERE id=$1::uuid AND ended_at IS NULL`, r.id, end, used)
			k.VerifyRemove(ctx, key, r.bridge, net.ParseIP(r.ip))
			continue
		}
		if used != r.used {
			_, _ = s.db.Exec(ctx, `UPDATE iam_v2.verification_access_grants SET used_bytes=$2 WHERE id=$1::uuid AND ended_at IS NULL AND used_bytes < $2`, r.id, used)
		}
		keep[key] = true
		admittedKeep[r.bridge+"|"+r.ip] = true
		if !st.Admitted[r.bridge+"|"+r.ip] || !st.Objects[key] {
			// RESTORE: what is left, never a fresh window.
			k.VerifyRemove(ctx, key, r.bridge, net.ParseIP(r.ip))
			if err := k.VerifyApply(ctx, nft.VerifyGrant{Key: key, Bridge: r.bridge, IP: net.ParseIP(r.ip),
				Remaining: time.Until(r.expires), CapBytes: r.capBytes, UsedBytes: used, UpKbps: r.up, DownKbps: r.down}); err != nil {
				slog.Warn("temporary access: restore failed", "err", err)
			} else {
				slog.Info("temporary access restored", "remaining_s", int(time.Until(r.expires).Seconds()))
			}
		}
	}
	// STRAYS: kernel objects or admissions no live grant claims (an ended grant whose removal was interrupted).
	strayKeys := map[string]bool{}
	for key := range st.Objects {
		if !keep[key] {
			strayKeys[key] = true
		}
	}
	strayAdmit := map[string]bool{}
	for a := range st.Admitted {
		if !admittedKeep[a] {
			strayAdmit[a] = true
		}
	}
	if len(strayKeys) == 0 && len(strayAdmit) == 0 {
		return
	}
	ended, err := s.db.Query(ctx, `SELECT id::text, bridge, host(ip) FROM iam_v2.verification_access_grants
	    WHERE tenant_id=$1 AND site_id=$2 AND ended_at IS NOT NULL AND ended_at > now() - interval '3 days'`, s.tenID, s.siteID)
	if err != nil {
		return
	}
	for ended.Next() {
		var id, bridge, ip string
		if ended.Scan(&id, &bridge, &ip) != nil {
			continue
		}
		key := grantKey(id)
		if strayKeys[key] || strayAdmit[bridge+"|"+ip] {
			if !admittedKeep[bridge+"|"+ip] {
				k.VerifyRemove(ctx, key, bridge, net.ParseIP(ip))
			} else {
				k.VerifyRemove(ctx, key, "", nil)
			}
			delete(strayKeys, key)
			delete(strayAdmit, bridge+"|"+ip)
		}
	}
	ended.Close()
	// An admission nobody can account for is removed outright (the maps are keyed by address alone).
	for a := range strayAdmit {
		if i := strings.IndexByte(a, '|'); i > 0 {
			k.VerifyRemove(ctx, "", a[:i], net.ParseIP(a[i+1:]))
		}
	}
}

func (s *server) verifyAccessLoop(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		s.reconcileVerifyAccess(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// POST /v1/auth/otp/temporary-access/cancel -- "use a different address": the device's window ends now.
func (s *server) cancelVerifyAccess(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IP  string `json:"ip"`
		MAC string `json:"mac"`
	}
	if err := decodeGuestAuthBody(r, &req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if _, err := net.ParseMAC(strings.TrimSpace(req.MAC)); err != nil {
		httpErr(w, http.StatusBadRequest, "device identity required")
		return
	}
	s.endVerifyAccess(r.Context(), req.MAC, "CANCELLED")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
