package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/commands"
)

// CommandsBase issues signed, allow-listed commands to appliances over NATS and
// tracks their durable lifecycle. Commands are signed with the DEDICATED
// command-signing key; delivery/results ride the per-appliance mTLS subjects.
type CommandsBase struct {
	*Base
	NC   *nats.Conn
	priv ed25519.PrivateKey
}

// NewCommandsBase loads the command-signing key; returns nil if unavailable.
func NewCommandsBase(base *Base, nc *nats.Conn, keyPath string) *CommandsBase {
	raw, err := os.ReadFile(keyPath)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil
	}
	return &CommandsBase{Base: base, NC: nc, priv: ed25519.PrivateKey(raw)}
}

func (b *CommandsBase) Routes() http.Handler {
	r := chi.NewRouter()
	reauth := RequireReauth(b.Redis)
	r.With(auth.RequirePermission("platform.commands.issue")).Get("/", b.list)
	// Issuing is permission-gated; controlled_reboot additionally requires a
	// password step-up (enforced in the handler).
	r.With(auth.RequirePermission("platform.commands.issue"), reauth).Post("/", b.issue)
	return r
}

type issueCmdReq struct {
	ApplianceID     string          `json:"appliance_id"`
	CommandType     string          `json:"command_type"`
	Params          json.RawMessage `json:"params"`
	ExpiresInSecond int             `json:"expires_in_seconds"`
	Reason          string          `json:"reason"`
	MaintenanceWin  json.RawMessage `json:"maintenance_window"`
}

func (b *CommandsBase) issue(w http.ResponseWriter, r *http.Request) {
	var in issueCmdReq
	if err := DecodeJSON(r, &in); err != nil || in.ApplianceID == "" || in.CommandType == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "appliance_id and command_type required")
		return
	}
	if !commands.Allowed[in.CommandType] {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "command_type not in allow-list")
		return
	}
	if in.Params == nil {
		in.Params = json.RawMessage(`{}`)
	}
	// restart_stayconnect_service param must be an allow-listed unit.
	if in.CommandType == "restart_stayconnect_service" {
		var p struct {
			Service string `json:"service"`
		}
		_ = json.Unmarshal(in.Params, &p)
		if !commands.RestartAllowList[p.Service] {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "service not in restart allow-list")
			return
		}
	}
	ttl := in.ExpiresInSecond
	if ttl <= 0 || ttl > 3600 {
		ttl = 300
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	cmdID := newUUIDv4()
	now := time.Now()
	env := &commands.Envelope{
		CommandID: cmdID, ApplianceID: in.ApplianceID, CommandType: in.CommandType,
		Params: in.Params, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Duration(ttl) * time.Second).Unix(),
		IdempotencyKey: cmdID,
	}
	commands.Sign(b.priv, env)

	operatorID := ""
	if s := auth.FromContext(r.Context()); s != nil {
		operatorID = s.OperatorID
	}
	_, err := b.DB.Exec(ctx, `
        INSERT INTO appliance_commands (command_id, appliance_id, command_type, params, issued_at, expires_at,
               signer_key_id, signature, status, issued_by, reason, maintenance_window)
        VALUES ($1,$2,$3,$4, to_timestamp($5), to_timestamp($6), $7,$8,'pending', NULLIF($9,'')::uuid, $10, $11)`,
		cmdID, in.ApplianceID, in.CommandType, in.Params, env.IssuedAt, env.ExpiresAt,
		env.SignerKeyID, env.Signature, operatorID, in.Reason, nullableJSON(in.MaintenanceWin))
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "command store failed: "+err.Error())
		return
	}
	// Publish over the appliance-specific mTLS subject.
	if b.NC != nil {
		payload, _ := json.Marshal(env)
		if err := b.NC.Publish("appliances."+in.ApplianceID+".commands", payload); err == nil {
			_ = b.NC.Flush()
			b.DB.Exec(ctx, `UPDATE appliance_commands SET status='delivered', delivered_at=now() WHERE command_id=$1`, cmdID)
		}
	}
	audit.Op(r.Context(), b.DB, r, "command.issued", "command", cmdID, map[string]any{
		"appliance_id": in.ApplianceID, "command_type": in.CommandType, "reason": in.Reason})
	WriteJSON(w, http.StatusCreated, map[string]any{"command_id": cmdID, "status": "delivered", "command_type": in.CommandType})
}

func (b *CommandsBase) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.DB.Query(ctx, `
        SELECT command_id::text, appliance_id::text, command_type, status, issued_at, COALESCE(completed_at, issued_at), COALESCE(result::text,'')
          FROM appliance_commands ORDER BY issued_at DESC LIMIT 200`)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, appID, ct, st, res string
		var iss, comp time.Time
		_ = rows.Scan(&id, &appID, &ct, &st, &iss, &comp, &res)
		m := map[string]any{"command_id": id, "appliance_id": appID, "command_type": ct, "status": st, "issued_at": iss}
		if res != "" {
			m["result"] = json.RawMessage(res)
		}
		out = append(out, m)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

// StartResultsConsumer subscribes to appliances.*.commands.results and records
// durable command results idempotently — the terminal state is only written
// once (repeated result uploads don't create a second effect).
func StartResultsConsumer(ctx context.Context, db *pgxpool.Pool, nc *nats.Conn) error {
	if nc == nil {
		return nil
	}
	sub, err := nc.Subscribe("appliances.*.commands.results", func(m *nats.Msg) {
		var res struct {
			CommandID string          `json:"command_id"`
			Status    string          `json:"status"`
			Result    json.RawMessage `json:"result"`
		}
		if json.Unmarshal(m.Data, &res) != nil || res.CommandID == "" {
			return
		}
		term := res.Status == "succeeded" || res.Status == "failed"
		wctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		// Idempotent: only transition out of a non-terminal state.
		_, _ = db.Exec(wctx, `
            UPDATE appliance_commands
               SET status=$2, result=$3, completed_at=CASE WHEN $4 THEN now() ELSE completed_at END,
                   acknowledged_at=COALESCE(acknowledged_at, now())
             WHERE command_id=$1 AND status NOT IN ('succeeded','failed','expired','cancelled')`,
			res.CommandID, res.Status, nullableJSON(json.RawMessage(res.Result)), term)
	})
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); _ = sub.Drain() }()
	return nil
}

func nullableJSON(j json.RawMessage) any {
	if len(j) == 0 || string(j) == "null" {
		return nil
	}
	return string(j)
}

// newUUIDv4 returns a random RFC-4122 v4 UUID string.
func newUUIDv4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b[0:4]) + "-" + hex.EncodeToString(b[4:6]) + "-" +
		hex.EncodeToString(b[6:8]) + "-" + hex.EncodeToString(b[8:10]) + "-" + hex.EncodeToString(b[10:16])
}
