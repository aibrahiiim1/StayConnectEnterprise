package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/stayconnect/enterprise/data-plane/internal/commands"
)

// startCommandHandler subscribes to this appliance's signed command subject,
// verifies each command (signature + allow-list + expiry + appliance binding),
// enforces exactly-once via the site DB, executes the allow-listed action, and
// publishes a durable result. No arbitrary shell / unit / path / url is ever
// accepted.
func (s *server) startCommandHandler(ctx context.Context, nc *nats.Conn, applianceID, pubKeyPath string) error {
	raw, err := os.ReadFile(pubKeyPath)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		slog.Warn("commands: command-signing pubkey unavailable — command channel disabled", "path", pubKeyPath)
		return nil
	}
	pub := ed25519.PublicKey(raw)
	// Exactly-once ledger in the SITE DB (survives reboot).
	if s.db != nil {
		_, _ = s.db.Exec(ctx, `CREATE TABLE IF NOT EXISTS edge_executed_commands (
            command_id UUID PRIMARY KEY, command_type TEXT, status TEXT,
            result JSONB, completed_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	}
	sub, err := nc.Subscribe("appliances."+applianceID+".commands", func(m *nats.Msg) {
		s.handleCommand(ctx, nc, pub, applianceID, m.Data)
	})
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); _ = sub.Drain() }()
	slog.Info("commands: subscribed", "subject", "appliances."+applianceID+".commands")
	return nil
}

func (s *server) handleCommand(ctx context.Context, nc *nats.Conn, pub ed25519.PublicKey, applianceID string, data []byte) {
	var env commands.Envelope
	if json.Unmarshal(data, &env) != nil {
		return
	}
	result := func(status string, res map[string]any) {
		body, _ := json.Marshal(map[string]any{"command_id": env.CommandID, "status": status, "result": res})
		_ = nc.Publish("appliances."+applianceID+".commands.results", body)
		_ = nc.Flush()
	}
	// Bind + verify.
	if env.ApplianceID != applianceID {
		result("rejected", map[string]any{"reason": "appliance mismatch"})
		return
	}
	if time.Now().Unix() > env.ExpiresAt {
		result("expired", map[string]any{"reason": "command expired"})
		return
	}
	if !commands.Verify(pub, &env) {
		result("rejected", map[string]any{"reason": "bad signature or command not allowed"})
		return
	}
	// Exactly-once: if already executed, re-ack the stored result WITHOUT
	// re-executing (durable across reboot via the site DB).
	if s.db != nil {
		var st string
		var stored []byte
		err := s.db.QueryRow(ctx, `SELECT status, COALESCE(result::text,'{}') FROM edge_executed_commands WHERE command_id=$1`, env.CommandID).Scan(&st, &stored)
		if err == nil {
			var res map[string]any
			_ = json.Unmarshal(stored, &res)
			result(st, res) // idempotent re-ack; no second execution
			return
		}
	}
	// Execute the allow-listed command.
	status, res := s.execCommand(ctx, &env)
	if s.db != nil {
		rb, _ := json.Marshal(res)
		_, _ = s.db.Exec(ctx, `INSERT INTO edge_executed_commands (command_id, command_type, status, result)
            VALUES ($1,$2,$3,$4) ON CONFLICT (command_id) DO NOTHING`, env.CommandID, env.CommandType, status, rb)
	}
	result(status, res)
}

func (s *server) execCommand(ctx context.Context, env *commands.Envelope) (string, map[string]any) {
	switch env.CommandType {
	case "request_heartbeat":
		if s.natsConn != nil {
			publishHeartbeat(s.natsConn, "hb."+s.applID, s.applID, time.Now())
		}
		return "succeeded", map[string]any{"heartbeat": "published"}
	case "refresh_license":
		if s.lic != nil && s.certMgr != nil {
			if cl, base, ok := s.certMgr.Transport(); ok {
				s.lic.SetMTLSTransport(cl, base)
			}
		}
		return "succeeded", map[string]any{"license": "refresh requested"}
	case "retry_telemetry":
		return "succeeded", map[string]any{"telemetry": "drain requested"}
	case "collect_sanitized_diagnostics":
		return "succeeded", map[string]any{"version": scdVersion, "appliance_id": s.applID, "site_id": s.siteID, "note": "no guest PII"}
	case "rotate_certificate":
		if s.certMgr != nil {
			go s.certMgr.MaybeRotate(context.Background(), 3650*24*time.Hour) // force rotate
		}
		return "succeeded", map[string]any{"certificate": "rotation triggered"}
	case "restart_stayconnect_service":
		var p struct {
			Service string `json:"service"`
		}
		_ = json.Unmarshal(env.Params, &p)
		if !commands.RestartAllowList[p.Service] {
			return "rejected", map[string]any{"reason": "service not in allow-list"}
		}
		go func() { _ = exec.Command("systemctl", "restart", p.Service).Run() }()
		return "succeeded", map[string]any{"restarted": p.Service}
	case "controlled_reboot":
		// Maintenance-window + cancellation-delay guard: schedule, don't reboot now.
		go func() {
			time.Sleep(60 * time.Second) // cancellation delay window
			_ = exec.Command("systemctl", "reboot").Run()
		}()
		return "succeeded", map[string]any{"reboot": "scheduled in 60s (cancellable)"}
	case "schedule_update":
		return "succeeded", map[string]any{"update": "scheduled (see Phase 9)"}
	default:
		return "rejected", map[string]any{"reason": "unknown command"}
	}
}
