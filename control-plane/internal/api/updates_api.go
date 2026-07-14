package api

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/updates"
)

// UpdatesBase signs software-update manifests (dedicated update-signing key) and
// publishes them to appliances over the per-appliance mTLS subject. Packages are
// built OFF the appliance and delivered with a SHA-256 the appliance verifies.
type UpdatesBase struct {
	*Base
	NC   *nats.Conn
	priv ed25519.PrivateKey
}

func NewUpdatesBase(base *Base, nc *nats.Conn, keyPath string) *UpdatesBase {
	raw, err := os.ReadFile(keyPath)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil
	}
	return &UpdatesBase{Base: base, NC: nc, priv: ed25519.PrivateKey(raw)}
}

func (b *UpdatesBase) Routes() http.Handler {
	r := chi.NewRouter()
	reauth := RequireReauth(b.Redis)
	r.With(auth.RequirePermission("platform.updates.manage")).Get("/", b.list)
	r.With(auth.RequirePermission("platform.updates.manage"), reauth).Post("/assign", b.assign)
	return r
}

type assignUpdateReq struct {
	ApplianceID      string `json:"appliance_id"`
	Component        string `json:"component"`
	Version          string `json:"version"`
	MinSourceVersion string `json:"min_source_version"`
	Model            string `json:"model"`
	Channel          string `json:"channel"`
	PackageB64       string `json:"package_b64"` // built off-appliance, delivered here
}

func (b *UpdatesBase) assign(w http.ResponseWriter, r *http.Request) {
	var in assignUpdateReq
	if err := DecodeJSON(r, &in); err != nil || in.ApplianceID == "" || in.Component == "" || in.Version == "" || in.PackageB64 == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "appliance_id, component, version, package_b64 required")
		return
	}
	pkg, err := base64.StdEncoding.DecodeString(in.PackageB64)
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "package_b64 invalid")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	updateID := newUUIDv4()
	m := &updates.Manifest{
		UpdateID: updateID, Component: in.Component, Version: in.Version,
		MinSourceVersion: in.MinSourceVersion, Model: in.Model, Channel: in.Channel,
		SHA256: updates.SHA256Hex(pkg), Size: int64(len(pkg)), ApplianceID: in.ApplianceID,
	}
	updates.Sign(b.priv, m)
	_, _ = b.DB.Exec(ctx, `
        INSERT INTO appliance_update_assignments (update_id, appliance_id, component, version, sha256, status)
        VALUES ($1,$2,$3,$4,$5,'assigned') ON CONFLICT (update_id) DO NOTHING`,
		updateID, in.ApplianceID, in.Component, in.Version, m.SHA256)
	if b.NC != nil {
		payload, _ := json.Marshal(map[string]any{"manifest": m, "package_b64": in.PackageB64})
		if err := b.NC.Publish("appliances."+in.ApplianceID+".updates", payload); err == nil {
			_ = b.NC.Flush()
			b.DB.Exec(ctx, `UPDATE appliance_update_assignments SET status='delivered' WHERE update_id=$1`, updateID)
		}
	}
	audit.Op(r.Context(), b.DB, r, "update.assigned", "update", updateID, map[string]any{
		"appliance_id": in.ApplianceID, "component": in.Component, "version": in.Version})
	WriteJSON(w, http.StatusCreated, map[string]any{"update_id": updateID, "sha256": m.SHA256, "status": "delivered"})
}

func (b *UpdatesBase) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.DB.Query(ctx, `
        SELECT update_id::text, appliance_id::text, component, version, status, COALESCE(result::text,'')
          FROM appliance_update_assignments ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, appID, comp, ver, st, res string
		_ = rows.Scan(&id, &appID, &comp, &ver, &st, &res)
		m := map[string]any{"update_id": id, "appliance_id": appID, "component": comp, "version": ver, "status": st}
		if res != "" {
			m["result"] = json.RawMessage(res)
		}
		out = append(out, m)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

// StartUpdateStatusConsumer records update status/results idempotently from
// appliances.*.updates.status.
func StartUpdateStatusConsumer(ctx context.Context, db *pgxpool.Pool, nc *nats.Conn) error {
	if nc == nil {
		return nil
	}
	sub, err := nc.Subscribe("appliances.*.updates.status", func(m *nats.Msg) {
		var st struct {
			UpdateID string          `json:"update_id"`
			Status   string          `json:"status"`
			Result   json.RawMessage `json:"result"`
		}
		if json.Unmarshal(m.Data, &st) != nil || st.UpdateID == "" {
			return
		}
		wctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		_, _ = db.Exec(wctx, `
            UPDATE appliance_update_assignments SET status=$2, result=$3, completed_at=now()
             WHERE update_id=$1 AND status NOT IN ('succeeded','failed')`,
			st.UpdateID, st.Status, nullableJSON(json.RawMessage(st.Result)))
	})
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); _ = sub.Drain() }()
	return nil
}
