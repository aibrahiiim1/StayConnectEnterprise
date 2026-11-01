package portaltls

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// The worker does nothing until Central is configured (Availability).
const WorkerInterval = 30 * time.Second

const (
	maxAttempts    = 5
	processingRent = 20 * time.Minute // lease on a PROCESSING order before another pass may reclaim it
	issueBudget    = 10 * time.Minute // one ACME conversation, propagation included
	capWindow      = 7 * 24 * time.Hour
)

// Backoff is the wait after the n-th failure (n >= 1); the fifth failure is final.
func Backoff(n int) time.Duration {
	switch {
	case n <= 1:
		return 5 * time.Minute
	case n == 2:
		return 15 * time.Minute
	case n == 3:
		return time.Hour
	default:
		return 4 * time.Hour
	}
}

// WeeklyCap decides whether one more NEW certificate may be issued now (an issuance in progress counts as
// now). At the cap it returns when enough have aged out of the rolling window.
func WeeklyCap(limit int, counted []time.Time, now time.Time) (bool, time.Time) {
	var live []time.Time
	for _, t := range counted {
		if now.Sub(t) < capWindow {
			live = append(live, t)
		}
	}
	if len(live) < limit {
		return true, now
	}
	sort.Slice(live, func(i, j int) bool { return live[i].Before(live[j]) })
	// One more needs len(live)-limit+1 gone; the last of those is live[len(live)-limit].
	return false, live[len(live)-limit].Add(capWindow)
}

func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(WorkerInterval)
	defer t.Stop()
	for {
		s.safeTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.nudge:
		}
	}
}

// safeTick runs one pass; a panic is logged and the worker carries on with the next pass (audit Phase 1 L19).
func (s *Service) safeTick(ctx context.Context) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("portal tls: worker pass panicked; continuing", "panic", p, "stack", string(debug.Stack()))
		}
	}()
	s.Tick(ctx)
}

func (s *Service) Tick(ctx context.Context) {
	st, ok := s.available(ctx)
	if !ok {
		return
	}
	prov, err := s.NewProvider(st)
	if err != nil {
		slog.Warn("portal tls: DNS provider unavailable", "err", err)
		return
	}
	s.syncAllDNS(ctx, prov)
	if s.acquireACME(ctx) != nil {
		return
	}
	func() {
		defer s.releaseACME()
		_ = s.processRevocations(ctx, &st, "")
	}()
	for i := 0; i < 20 && ctx.Err() == nil; i++ {
		worked, err := s.processNextOrder(ctx, &st, prov)
		if err != nil {
			slog.Warn("portal tls: order pass failed", "err", err)
			break
		}
		if !worked {
			break
		}
	}
	s.refreshARI(ctx)
	s.recheckDomains(ctx, st)
}

func (s *Service) syncAllDNS(ctx context.Context, prov DNSProvider) {
	rows, err := s.DB.Query(ctx, `
        SELECT `+hostCols+` FROM portal_hostnames
         WHERE (released_at IS NULL AND portal_ip IS NOT NULL
                AND (dns_record_id IS NULL OR dns_synced_ip IS DISTINCT FROM portal_ip OR dns_error IS NOT NULL))
            OR (released_at IS NOT NULL AND dns_record_id IS NOT NULL)
         LIMIT 200`)
	if err != nil {
		slog.Warn("portal tls: DNS work query failed", "err", err)
		return
	}
	var work []hostRow
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			rows.Close()
			slog.Warn("portal tls: DNS work row unreadable", "err", err)
			return
		}
		work = append(work, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Warn("portal tls: DNS work query failed", "err", err)
		return
	}
	for _, h := range work {
		if ctx.Err() != nil {
			return
		}
		dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		s.syncHostDNS(dctx, prov, h)
		cancel()
	}
}

func (s *Service) processNextOrder(ctx context.Context, st *Settings, prov DNSProvider) (bool, error) {
	dirURL := s.Opts.directoryURL(st.ACMEDirectory)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	var id, applianceID, hostname, kind string
	var csrDER []byte
	var attempts int
	err = tx.QueryRow(ctx, `
        SELECT id::text, appliance_id::text, hostname, kind, csr_der, attempts
          FROM portal_certificate_orders
         WHERE status IN ('QUEUED','PROCESSING') AND next_attempt_at <= now()
         ORDER BY next_attempt_at, created_at
         LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id, &applianceID, &hostname, &kind, &csrDER, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// attempts counts claims, so an order whose pass never finished (a crash or restart mid-order) still reaches
	// maxAttempts instead of being reclaimed for ever (audit Phase 1 M12).
	if attempts >= maxAttempts {
		if _, err := tx.Exec(ctx, `UPDATE portal_certificate_orders SET status='FAILED', error=$2 WHERE id=$1::uuid`,
			id, fmt.Sprintf("gave up after %d attempts that did not finish", attempts)); err != nil {
			return false, err
		}
		AppendEvent(ctx, tx, applianceID, hostname, EvOrderFailed, ActorSystem, map[string]any{
			"order_id": id, "attempt": attempts, "error": "attempts did not finish (interrupted)"})
		return true, tx.Commit(ctx)
	}

	countsNew := false
	if kind == KindOneGate {
		// NEW = no earlier certificate for this hostname from THIS directory (renewals are exempt from the CA's
		// limit); a staging certificate does not make a production one a renewal.
		// Per appliance: every appliance orders the shared name, and each one's first certificate for it is new.
		if err := tx.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM portal_certificate_orders
		        WHERE appliance_id=$3::uuid AND hostname=$1 AND issued_at IS NOT NULL AND acme_directory_url=$2)`,
			hostname, dirURL, applianceID).Scan(&countsNew); err != nil {
			return false, err
		}
	}
	if countsNew {
		// One cap decision at a time across every ctrlapi sharing this database.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('portal_tls_weekly_cap'))`); err != nil {
			return false, err
		}
		rows, err := tx.Query(ctx, `
            SELECT CASE WHEN status='PROCESSING' THEN now() ELSE issued_at END
              FROM portal_certificate_orders
             WHERE kind='ONEGATE' AND counts_new AND id<>$1::uuid
               AND (issued_at > now() - interval '7 days' OR status='PROCESSING')`, id)
		if err != nil {
			return false, err
		}
		var counted []time.Time
		for rows.Next() {
			var t time.Time
			if err := rows.Scan(&t); err != nil {
				rows.Close()
				return false, err
			}
			counted = append(counted, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return false, err
		}
		var now time.Time
		_ = tx.QueryRow(ctx, `SELECT now()`).Scan(&now)
		if ok, retryAt := WeeklyCap(st.WeeklyNewLimit, counted, now); !ok {
			if _, err := tx.Exec(ctx, `UPDATE portal_certificate_orders SET status='QUEUED', next_attempt_at=$2 WHERE id=$1::uuid`, id, retryAt); err != nil {
				return false, err
			}
			AppendEvent(ctx, tx, applianceID, hostname, EvOrderDeferred, ActorSystem, map[string]any{
				"order_id": id, "until": retryAt, "weekly_new_limit": st.WeeklyNewLimit})
			return true, tx.Commit(ctx)
		}
	}
	if _, err := tx.Exec(ctx, `
        UPDATE portal_certificate_orders
           SET status='PROCESSING', counts_new=$2, next_attempt_at=now() + $3::interval, acme_directory_url=$4,
               attempts=attempts+1
         WHERE id=$1::uuid`, id, countsNew, fmt.Sprintf("%d seconds", int(processingRent.Seconds())), dirURL); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}

	res, permanent, err := s.runOrder(ctx, st, prov, dirURL, id, applianceID, hostname, kind, csrDER)
	// The outcome is recorded even when shutdown cancels ctx: a certificate issued just before is never lost
	// and ordered again (audit Phase 1 M12).
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err != nil {
		s.failOrder(rctx, id, applianceID, hostname, attempts+1, permanent, err)
		return true, nil
	}
	s.completeOrder(rctx, id, applianceID, hostname, res)
	return true, nil
}

// runOrder obtains the certificate if the order is still wanted. permanent marks a failure no retry can fix.
func (s *Service) runOrder(ctx context.Context, st *Settings, prov DNSProvider, dirURL, id, applianceID, hostname, kind string,
	csrDER []byte) (*issued, bool, error) {
	challengeLabel := ""
	identifiers := []string{hostname}
	challengeFor := func(id string) string { return ChallengeNameFor(kind, id, challengeLabel, st.ZoneName) }
	sp := s.Opts.Shared()
	switch {
	case kind == KindOneGate && sp.Enabled() && hostname == sp.Hostname:
		var own, portalIP string
		var released *time.Time
		err := s.DB.QueryRow(ctx, `SELECT fqdn, COALESCE(host(portal_ip),''), released_at FROM portal_hostnames WHERE appliance_id=$1`,
			applianceID).Scan(&own, &portalIP, &released)
		if err != nil || released != nil {
			return nil, true, errors.New("this appliance no longer has a live OneGate hostname")
		}
		if portalIP != sp.Address.String() {
			return nil, true, fmt.Errorf("the appliance's portal address %s is no longer the shared address %s", portalIP, sp.Address)
		}
		if ok, why := s.sharedReady(ctx, *st, false); !ok {
			return nil, false, fmt.Errorf("the shared portal name is not ready in public DNS: %s", why)
		}
		identifiers = []string{hostname, own}
		challengeFor = func(id string) string {
			if id == sp.Hostname {
				return SharedChallengeName(st.ZoneName)
			}
			return "_acme-challenge." + id
		}
	case kind == KindOneGate:
		var released *time.Time
		err := s.DB.QueryRow(ctx, `SELECT released_at FROM portal_hostnames WHERE appliance_id=$1 AND fqdn=$2`, applianceID, hostname).Scan(&released)
		if err != nil || released != nil {
			return nil, true, errors.New("the hostname is no longer this appliance's")
		}
	case kind == KindCustom:
		d, err := s.liveDomain(ctx, s.DB, applianceID)
		if err != nil {
			return nil, false, err
		}
		if d == nil || d.FQDN != hostname || d.Status != "VERIFIED" {
			return nil, true, errors.New("the custom domain is no longer verified for this appliance")
		}
		challengeLabel = d.ChallengeLabel
	}
	if err := s.acquireACME(ctx); err != nil {
		return nil, false, err
	}
	defer s.releaseACME()
	ictx, cancel := context.WithTimeout(ctx, issueBudget)
	defer cancel()
	c, err := s.acmeClient(ictx, st, dirURL)
	if err != nil {
		return nil, false, err
	}
	res, err := s.issue(ictx, c, prov, identifiers, challengeFor, csrDER,
		func(u string) {
			_, _ = s.DB.Exec(ctx, `UPDATE portal_certificate_orders SET acme_order_url=$2 WHERE id=$1::uuid`, id, u)
		})
	if err != nil {
		s.forgetAccountIfGone(ctx, err)
		return nil, false, err
	}
	leaf, err := leafFromChain(res.ChainPEM)
	if err != nil {
		return nil, false, err
	}
	res.RenewAfter, _ = RenewAfter(ctx, s.acmeHTTP(), dirURL, leaf)
	return res, false, nil
}

func (s *Service) completeOrder(ctx context.Context, id, applianceID, hostname string, res *issued) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		slog.Error("portal tls: could not record an issued certificate", "order_id", id, "err", err)
		return
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
        UPDATE portal_certificate_orders
           SET status='ISSUED', issued_at=now(), not_before=$2, not_after=$3, serial=$4, chain_pem=$5,
               renew_after=$6, ari_checked_at=now(), acme_order_url=$7, error=NULL
         WHERE id=$1::uuid AND status='PROCESSING'`,
		id, res.NotBefore, res.NotAfter, res.Serial, res.ChainPEM, res.RenewAfter, res.OrderURL)
	if err != nil {
		slog.Error("portal tls: could not record an issued certificate", "order_id", id, "err", err)
		return
	}
	if tag.RowsAffected() == 0 {
		// Cancelled while the CA was working: the certificate exists anyway, so record and revoke it.
		_, _ = tx.Exec(ctx, `
            UPDATE portal_certificate_orders
               SET issued_at=now(), not_before=$2, not_after=$3, serial=$4, chain_pem=$5,
                   revoke_requested_at=now(), revoke_reason=$6
             WHERE id=$1::uuid`, id, res.NotBefore, res.NotAfter, res.Serial, res.ChainPEM, RevokeCessation)
		AppendEvent(ctx, tx, applianceID, hostname, EvRevokeRequested, ActorSystem, map[string]any{
			"order_id": id, "serial": res.Serial, "reason": RevokeCessation, "why": "issued after the order was cancelled"})
		_ = tx.Commit(ctx)
		s.Nudge()
		return
	}
	_, _ = tx.Exec(ctx, `
        UPDATE portal_certificate_orders SET status='SUPERSEDED'
         WHERE appliance_id=$1 AND hostname=$2 AND status='ISSUED' AND id<>$3::uuid`, applianceID, hostname, id)
	AppendEvent(ctx, tx, applianceID, hostname, EvOrderIssued, ActorSystem, map[string]any{
		"order_id": id, "serial": res.Serial, "not_after": res.NotAfter, "renew_after": res.RenewAfter})
	if err := tx.Commit(ctx); err != nil {
		slog.Error("portal tls: could not record an issued certificate", "order_id", id, "err", err)
	}
}

func (s *Service) failOrder(ctx context.Context, id, applianceID, hostname string, attempts int, permanent bool, cause error) {
	msg := truncate(cause.Error(), 1000)
	final := permanent || attempts >= maxAttempts
	status := "QUEUED"
	if final {
		status = "FAILED"
	}
	next := time.Now().Add(Backoff(attempts))
	_, _ = s.DB.Exec(ctx, `
        UPDATE portal_certificate_orders SET status=$2, attempts=$3, next_attempt_at=$4, error=$5
         WHERE id=$1::uuid AND status='PROCESSING'`, id, status, attempts, next, msg)
	kind := EvOrderRetry
	detail := map[string]any{"order_id": id, "attempt": attempts, "error": msg}
	if final {
		kind = EvOrderFailed
	} else {
		detail["next_attempt_at"] = next
	}
	AppendEvent(ctx, s.DB, applianceID, hostname, kind, ActorSystem, detail)
	slog.Warn("portal tls: certificate order failed", "order_id", id, "hostname", hostname, "attempt", attempts, "final", final, "err", msg)
}

// processRevocations revokes requested certificates (only one appliance's when applianceID is set). It returns
// the first failure but attempts the rest. Caller holds acmeMu.
func (s *Service) processRevocations(ctx context.Context, st *Settings, applianceID string) error {
	rows, err := s.DB.Query(ctx, `
        SELECT id::text, appliance_id::text, hostname, chain_pem, COALESCE(acme_directory_url,''),
               COALESCE(revoke_reason,''), not_after, COALESCE(error,'')
          FROM portal_certificate_orders
         WHERE revoke_requested_at IS NOT NULL AND revoked_at IS NULL AND chain_pem IS NOT NULL
           AND ($1='' OR appliance_id::text=$1)
         ORDER BY revoke_requested_at LIMIT 50`, applianceID)
	if err != nil {
		return err
	}
	type rv struct {
		id, app, host, chain, dir, reason, prevErr string
		notAfter                                   *time.Time
	}
	var work []rv
	for rows.Next() {
		var r rv
		if err := rows.Scan(&r.id, &r.app, &r.host, &r.chain, &r.dir, &r.reason, &r.notAfter, &r.prevErr); err != nil {
			rows.Close()
			return err
		}
		work = append(work, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var first error
	for _, r := range work {
		if r.notAfter != nil && r.notAfter.Before(time.Now()) {
			// Expired: the CA would refuse to revoke it.
			_, _ = s.DB.Exec(ctx, `UPDATE portal_certificate_orders SET status='REVOKED', revoked_at=now() WHERE id=$1::uuid`, r.id)
			AppendEvent(ctx, s.DB, r.app, r.host, EvCertRevoked, ActorSystem, map[string]any{"order_id": r.id, "note": "already expired"})
			continue
		}
		dir := r.dir
		if dir == "" {
			dir = s.Opts.directoryURL(st.ACMEDirectory)
		}
		err := func() error {
			leaf, err := leafFromChain(r.chain)
			if err != nil {
				return err
			}
			rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			c, err := s.acmeClient(rctx, st, dir)
			if err != nil {
				return err
			}
			return revokeCert(rctx, c, leaf.Raw, r.reason)
		}()
		if err != nil {
			msg := truncate("revocation: "+err.Error(), 1000)
			if msg != r.prevErr {
				AppendEvent(ctx, s.DB, r.app, r.host, EvRevokeError, ActorSystem, map[string]any{"order_id": r.id, "error": msg})
			}
			_, _ = s.DB.Exec(ctx, `UPDATE portal_certificate_orders SET error=$2 WHERE id=$1::uuid`, r.id, msg)
			if first == nil {
				first = err
			}
			continue
		}
		_, _ = s.DB.Exec(ctx, `UPDATE portal_certificate_orders SET status='REVOKED', revoked_at=now() WHERE id=$1::uuid`, r.id)
		AppendEvent(ctx, s.DB, r.app, r.host, EvCertRevoked, ActorSystem, map[string]any{"order_id": r.id, "reason": r.reason})
	}
	return first
}

// refreshARI asks the CA daily whether a certificate's renewal window moved. A renew_after inside the window is
// kept (re-drawing would make it wander); one outside is re-drawn, which carries a CA-announced revocation to
// the appliance as an early renewal.
func (s *Service) refreshARI(ctx context.Context) {
	// Current directory only: a certificate from a directory Central left is already due, and its old CA's
	// window must not push that back out.
	st, err := LoadSettings(ctx, s.DB)
	if err != nil {
		return
	}
	rows, err := s.DB.Query(ctx, `
        SELECT id::text, appliance_id::text, hostname, chain_pem, COALESCE(acme_directory_url,''), renew_after
          FROM portal_certificate_orders
         WHERE status='ISSUED' AND revoke_requested_at IS NULL AND not_after > now()
           AND acme_directory_url = $1
           AND (ari_checked_at IS NULL OR ari_checked_at < now() - interval '24 hours')
         LIMIT 50`, s.Opts.directoryURL(st.ACMEDirectory))
	if err != nil {
		return
	}
	type cert struct {
		id, app, host, chain, dir string
		renewAfter                *time.Time
	}
	var work []cert
	for rows.Next() {
		var c cert
		if err := rows.Scan(&c.id, &c.app, &c.host, &c.chain, &c.dir, &c.renewAfter); err != nil {
			rows.Close()
			slog.Warn("portal tls: renewal-info row unreadable", "err", err)
			return
		}
		work = append(work, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Warn("portal tls: renewal-info query failed", "err", err)
		return
	}
	hc := s.acmeHTTP()
	for _, c := range work {
		leaf, err := leafFromChain(c.chain)
		if err != nil || c.dir == "" {
			_, _ = s.DB.Exec(ctx, `UPDATE portal_certificate_orders SET ari_checked_at=now() WHERE id=$1::uuid`, c.id)
			continue
		}
		actx, cancel := context.WithTimeout(ctx, 30*time.Second)
		start, end, ok := ARIWindow(actx, hc, c.dir, leaf)
		cancel()
		if ok && (c.renewAfter == nil || c.renewAfter.Before(start) || c.renewAfter.After(end)) {
			ra := pickInWindow(start, end)
			_, _ = s.DB.Exec(ctx, `UPDATE portal_certificate_orders SET renew_after=$2, ari_checked_at=now() WHERE id=$1::uuid`, c.id, ra)
			AppendEvent(ctx, s.DB, c.app, c.host, EvARIUpdated, ActorSystem, map[string]any{
				"order_id": c.id, "renew_after": ra, "window_start": start, "window_end": end})
			continue
		}
		_, _ = s.DB.Exec(ctx, `UPDATE portal_certificate_orders SET ari_checked_at=now() WHERE id=$1::uuid`, c.id)
	}
}

// recheckDomains re-verifies VERIFIED domains daily; a broken delegation becomes FAILED and renewals stop.
func (s *Service) recheckDomains(ctx context.Context, st Settings) {
	rows, err := s.DB.Query(ctx, `
        SELECT d.id::text, d.appliance_id::text, d.fqdn, d.verify_token, d.challenge_label, d.status, d.last_checked_at,
               COALESCE(d.last_error,''), h.fqdn
          FROM portal_custom_domains d
          JOIN portal_hostnames h ON h.appliance_id = d.appliance_id AND h.released_at IS NULL
         WHERE d.status='VERIFIED' AND (d.last_checked_at IS NULL OR d.last_checked_at < now() - interval '24 hours')
         LIMIT 20`)
	if err != nil {
		return
	}
	type item struct {
		d    domainRow
		host string
	}
	var work []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.d.ID, &it.d.ApplianceID, &it.d.FQDN, &it.d.VerifyToken, &it.d.ChallengeLabel, &it.d.Status,
			&it.d.LastCheckedAt, &it.d.LastError, &it.host); err != nil {
			rows.Close()
			slog.Warn("portal tls: domain recheck row unreadable", "err", err)
			return
		}
		work = append(work, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Warn("portal tls: domain recheck query failed", "err", err)
		return
	}
	for _, it := range work {
		s.checkAndRecordDomain(ctx, &it.d, it.host, st)
	}
}

// CanRelease refuses a deletion Central could not clean up after. Deleting cascades the portal rows the worker
// retries from, so with an unconfigured Central only "nothing to clean" may pass; otherwise a published A
// record or valid certificate would be orphaned. Call it before a deletion revokes anything.
func (s *Service) CanRelease(ctx context.Context, applianceID string) error {
	if s == nil {
		return nil
	}
	if _, ok := s.available(ctx); ok {
		return nil
	}
	var pending bool
	if err := s.DB.QueryRow(ctx, `SELECT
	        EXISTS (SELECT 1 FROM portal_hostnames WHERE appliance_id=$1 AND dns_record_id IS NOT NULL)
	     OR EXISTS (SELECT 1 FROM portal_certificate_orders WHERE appliance_id=$1 AND status='ISSUED'
	                   AND revoked_at IS NULL AND not_after > now())`, applianceID).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return errors.New("portal cleanup is pending (a published DNS record or an unrevoked certificate) and " +
			"Central's portal-certificate configuration is incomplete; restore it, then delete")
	}
	return nil
}

// FinalizeRelease deletes the A record and revokes certificates now, so deleting the appliance never leaves a
// live DNS name or valid certificate behind.
func (s *Service) FinalizeRelease(ctx context.Context, applianceID string) error {
	if s == nil {
		return nil
	}
	st, ok := s.available(ctx)
	if !ok {
		return s.CanRelease(ctx, applianceID)
	}
	prov, err := s.NewProvider(st)
	if err != nil {
		return err
	}
	h, err := scanHost(s.DB.QueryRow(ctx, `SELECT `+hostCols+` FROM portal_hostnames WHERE appliance_id=$1`, applianceID))
	if err == nil && h.ReleasedAt != nil {
		if h = s.syncHostDNS(ctx, prov, h); h.DNSError != "" {
			return errors.New("the portal DNS record could not be deleted: " + h.DNSError)
		}
	}
	if err := s.acquireACME(ctx); err != nil {
		return err
	}
	defer s.releaseACME()
	return s.processRevocations(ctx, &st, applianceID)
}
