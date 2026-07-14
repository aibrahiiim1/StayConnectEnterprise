package main

// NATS-based RPC surface for ctrlapi → scd calls (phase 5.2).
//
// The control plane publishes requests on `scd.{applianceID}.{method}` and
// receives replies via NATS's built-in request/reply mechanism.
//
// Methods mirror the existing HTTP handlers one-for-one so we can later
// retire the HTTP admin endpoints; for now both paths coexist.
//
// Reply envelope:
//   - successful calls: raw JSON payload matching the HTTP shape, with a
//     "Nats-Status: 200" message header
//   - errors: {"error": "...", "message": "..."} with a non-200 header
//
// Subjects:
//   scd.{id}.revoke       { ip, reason }
//   scd.{id}.pms.test     { name }
//   scd.{id}.pms.cache    { name, limit }
//   scd.{id}.pms.health   { name }

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

const natsStatusHeader = "Nats-Status"

// startNATSDispatcher opens a NATS connection, subscribes to this
// appliance's subject tree, and returns the *nats.Conn so the caller can
// Drain() it on shutdown. Non-fatal errors are logged; the caller decides
// whether to exit.
func startNATSDispatcher(ctx context.Context, s *server, url, applianceID string, tlsCfg *tls.Config) (*nats.Conn, error) {
	if url == "" {
		return nil, errors.New("scd-nats: empty url")
	}
	opts := []nats.Option{
		nats.Name("scd/" + applianceID),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, e error) {
			slog.Warn("nats disconnected", "err", e)
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			slog.Info("nats reconnected", "url", c.ConnectedUrl())
		}),
	}
	// mTLS transport: present the appliance client cert; NO username/password.
	if tlsCfg != nil {
		opts = append(opts, nats.Secure(tlsCfg))
	}
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, err
	}
	// Config-reload subscriber (phase 5.3). Fire-and-forget broadcast:
	// ctrlapi publishes on config.{tenantID}.pms when any pms_providers
	// row for this tenant changes; scd reloads its registry in-place.
	// We use QueueSubscribe so if this appliance ever runs multiple scds
	// (HA, phase 5.5) only one does the reload per event.
	cfgSubj := "config." + s.tenID + ".pms"
	if _, err := nc.QueueSubscribe(cfgSubj, "scd-reload-pms", func(m *nats.Msg) {
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := s.reloadPMS(rctx); err != nil {
			slog.Error("pms reload from nats event failed", "err", err)
		}
	}); err != nil {
		nc.Close()
		return nil, err
	}
	slog.Info("scd nats config reload subscribed", "subject", cfgSubj)

	base := "scd." + applianceID + "."
	subs := []struct {
		subj string
		fn   func(context.Context, []byte) ([]byte, int)
	}{
		{base + "revoke", s.natsRevoke},
		{base + "pms.test", s.natsPMSTest},
		{base + "pms.cache", s.natsPMSCache},
		{base + "pms.health", s.natsPMSHealth},
	}
	for _, sub := range subs {
		subj := sub.subj
		fn := sub.fn
		if _, err := nc.Subscribe(subj, func(m *nats.Msg) {
			// Per-request timeout matches the HTTP side (10s for the slowest
			// of these, connection tests).
			rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			body, status := fn(rctx, m.Data)
			if status >= 500 {
				slog.Warn("nats handler error", "subj", subj, "status", status)
			}
			reply := &nats.Msg{Subject: m.Reply, Data: body, Header: nats.Header{}}
			reply.Header.Set(natsStatusHeader, strconv.Itoa(status))
			if err := nc.PublishMsg(reply); err != nil {
				slog.Warn("nats reply failed", "subj", subj, "err", err)
			}
		}); err != nil {
			nc.Close()
			return nil, err
		}
	}
	slog.Info("scd nats subscribed", "prefix", base)

	// Phase 5.4 — heartbeat publisher. Fire-and-forget every 10s on
	// hb.{applianceID}; ctrlapi's consumer flips the appliance row to
	// status=online and bumps last_seen_at.
	go heartbeatLoop(ctx, nc, applianceID)

	return nc, nil
}

// heartbeat interval + payload live here so they're adjacent to the subject
// name ("hb.X") they ride on.
const heartbeatInterval = 10 * time.Second

type heartbeat struct {
	ApplianceID   string `json:"appliance_id"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	Version       string `json:"version"`
}

// scdVersion is a compile-time baseline; swap to -ldflags injection later.
const scdVersion = "0.0.3-dev"

func heartbeatLoop(ctx context.Context, nc *nats.Conn, applianceID string) {
	subj := "hb." + applianceID
	started := time.Now()
	// Fire an immediate heartbeat so the appliance flips to `online` right
	// after boot; don't make the UI wait a full interval.
	publishHeartbeat(nc, subj, applianceID, started)
	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			publishHeartbeat(nc, subj, applianceID, started)
		}
	}
}

func publishHeartbeat(nc *nats.Conn, subj, applianceID string, started time.Time) {
	body, _ := json.Marshal(heartbeat{
		ApplianceID:   applianceID,
		UptimeSeconds: int64(time.Since(started).Seconds()),
		Version:       scdVersion,
	})
	if err := nc.Publish(subj, body); err != nil {
		slog.Warn("heartbeat publish failed", "err", err)
	}
}

// ----- handlers -----

type natsRevokeReq struct {
	IP     string `json:"ip"`
	Reason string `json:"reason"`
}

func (s *server) natsRevoke(ctx context.Context, raw []byte) ([]byte, int) {
	var req natsRevokeReq
	if err := json.Unmarshal(raw, &req); err != nil {
		return jerr("bad_request", "bad body"), 400
	}
	ip := net.ParseIP(req.IP)
	if ip == nil {
		return jerr("bad_request", "bad ip"), 400
	}
	nc := s.resolveNetwork(ctx, ip)
	if err := s.nft.Deny(ctx, nc.Bridge, ip); err != nil {
		slog.Error("nft deny", "err", err)
	}
	if err := s.shp.DeleteSession(ctx, nc.Bridge, ip); err != nil {
		slog.Warn("shape delete", "err", err)
	}
	reason := req.Reason
	if reason == "" {
		reason = "admin"
	}
	if err := s.sess.End(ctx, ip, reason); err != nil {
		slog.Error("nats revoke: session end", "err", err)
		return jerr("internal", "session end failed"), 500
	}
	return jok(map[string]string{"status": "revoked"}), 200
}

type natsPMSNameReq struct {
	Name  string `json:"name"`
	Limit int    `json:"limit,omitempty"`
}

func (s *server) natsPMSTest(ctx context.Context, raw []byte) ([]byte, int) {
	var req natsPMSNameReq
	if err := json.Unmarshal(raw, &req); err != nil {
		return jerr("bad_request", "bad body"), 400
	}
	prov, ok := s.currentPMSReg().Get(req.Name)
	if !ok {
		return jerr("not_found", "provider not registered"), 404
	}
	t, ok := prov.(pms.Tester)
	if !ok {
		return jerr("not_implemented", "provider doesn't support TestConnection"), 501
	}
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := t.TestConnection(tctx); err != nil {
		// Match HTTP behaviour: return 502 with the opaque error payload.
		return jok(pmsTestResp{
			OK: false, LatencyMS: time.Since(start).Milliseconds(), Error: err.Error(),
		}), 502
	}
	return jok(pmsTestResp{OK: true, LatencyMS: time.Since(start).Milliseconds()}), 200
}

func (s *server) natsPMSCache(ctx context.Context, raw []byte) ([]byte, int) {
	_ = ctx
	var req natsPMSNameReq
	if err := json.Unmarshal(raw, &req); err != nil {
		return jerr("bad_request", "bad body"), 400
	}
	prov, ok := s.currentPMSReg().Get(req.Name)
	if !ok {
		return jerr("not_found", "provider not registered"), 404
	}
	c, ok := prov.(pms.Cacher)
	if !ok {
		return jerr("not_implemented", "provider doesn't support cache snapshot"), 501
	}
	rows := c.CacheSnapshot(req.Limit)
	return jok(map[string]any{
		"provider": req.Name, "kind": prov.Kind(), "count": len(rows), "rows": rows,
	}), 200
}

func (s *server) natsPMSHealth(ctx context.Context, raw []byte) ([]byte, int) {
	_ = ctx
	var req natsPMSNameReq
	if err := json.Unmarshal(raw, &req); err != nil {
		return jerr("bad_request", "bad body"), 400
	}
	prov, ok := s.currentPMSReg().Get(req.Name)
	if !ok {
		return jerr("not_found", "provider not registered"), 404
	}
	return jok(map[string]any{
		"provider": req.Name, "kind": prov.Kind(), "health": prov.Health(),
	}), 200
}

// ----- tiny helpers -----

func jok(v any) []byte { b, _ := json.Marshal(v); return b }
func jerr(code, msg string) []byte {
	b, _ := json.Marshal(map[string]string{"error": code, "message": msg})
	return b
}
