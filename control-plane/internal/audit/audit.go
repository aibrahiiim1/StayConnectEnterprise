// Package audit records operator and system actions to audit_log for
// compliance and operational forensics. Writes are synchronous but never
// fail the caller; a write failure is logged and swallowed.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/clientip"
)

type Entry struct {
	TenantID   string
	ActorType  string // operator | system | appliance
	ActorID    string
	Action     string // e.g. "site.created", "operator.role_added"
	TargetType string
	TargetID   string
	IP         string
	UserAgent  string
	Payload    map[string]any
}

// Emit writes the entry; callers must not fail on errors.
func Emit(ctx context.Context, db *pgxpool.Pool, e Entry) {
	if e.Action == "" {
		return
	}
	if e.ActorType == "" {
		e.ActorType = "system"
	}
	var payload []byte
	if len(e.Payload) > 0 {
		payload, _ = json.Marshal(e.Payload)
	} else {
		payload = []byte("{}")
	}

	writeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	_, err := db.Exec(writeCtx, `
        INSERT INTO audit_log
          (ts, tenant_id, actor_type, actor_id, action, target_type, target_id, ip, user_agent, payload)
        VALUES
          (now(), NULLIF($1,'')::uuid, $2, NULLIF($3,''), $4, NULLIF($5,''), NULLIF($6,''),
           CASE WHEN $7 = '' THEN NULL ELSE $7::inet END,
           NULLIF($8,''), $9::jsonb)
    `, e.TenantID, e.ActorType, e.ActorID, e.Action, e.TargetType, e.TargetID, e.IP, e.UserAgent, string(payload))
	if err != nil {
		slog.Warn("audit emit failed", "action", e.Action, "err", err)
	}
}

// Op derives actor, IP and user agent from the request and emits. action is `<resource>.<verb>`; call after the
// main operation commits.
func Op(ctx context.Context, db *pgxpool.Pool, r *http.Request, action, targetType, targetID string, payload map[string]any) {
	e := Entry{
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Payload:    payload,
		IP:         clientip.From(r),
		UserAgent:  r.UserAgent(),
	}
	if s := auth.FromContext(r.Context()); s != nil {
		e.ActorType = "operator"
		e.ActorID = s.OperatorID
		e.TenantID = s.DefaultTenantID
	}
	// payload["_tenant_id"] overrides the session's tenant (e.g. a platform admin acting on a specific one).
	if tid, _ := payload["_tenant_id"].(string); tid != "" {
		e.TenantID = tid
		delete(payload, "_tenant_id")
	}
	Emit(ctx, db, e)
}

// System emits an entry for an action taken by the platform itself rather than an operator.
func System(ctx context.Context, db *pgxpool.Pool, action, targetType, targetID string, payload map[string]any) {
	Emit(ctx, db, Entry{
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Payload:    payload,
		ActorType:  "system",
	})
}
