// Package api holds HTTP handlers for the control-plane admin API.
package api

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	redis "github.com/redis/go-redis/v9"

	"github.com/stayconnect/enterprise/control-plane/internal/licensing"
	"github.com/stayconnect/enterprise/control-plane/internal/pki"
)

type Base struct {
	DB *pgxpool.Pool

	// LimitsDB (optional) is where commercial limits live when DB points at
	// a site database (deprecated guest-domain compatibility adapters):
	// counts run against DB, GetIntLimit against LimitsDB. nil = DB.
	LimitsDB *pgxpool.Pool

	// GuestDB (optional) is the inverse: for cloud-domain handlers that
	// need a read of site-owned tables (e.g. appliance effective-config).
	// nil = DB.
	GuestDB *pgxpool.Pool

	// Redis (optional) backs step-up re-authentication checks (RequireReauth).
	Redis *redis.Client

	// AssignKey (optional) is the vendor Ed25519 key used to sign appliance
	// assignment documents. nil disables signed-assignment issuance (the
	// lifecycle handlers then only update the Central DB, legacy behavior).
	AssignKey ed25519.PrivateKey

	// One-click activation composes assign + certificate + license. These are
	// nil/zero unless wired for the lifecycle routes.
	CA          *pki.CA
	ClientValid time.Duration      // issued client-cert lifetime
	Lic         *licensing.Service // hardware-bound license issuance
}

// issueAssignment signs + persists a new current assignment for the appliance
// (bumping its version) when an AssignKey is configured. Errors are non-fatal to
// the operator action but are surfaced to the caller for logging.
func (b *Base) issueAssignment(ctx context.Context, applianceID, state string) error {
	if b.AssignKey == nil {
		return nil
	}
	ab := &AssignmentBase{Base: b, SignKey: b.AssignKey}
	_, err := ab.Issue(ctx, applianceID, state)
	return err
}

func (b *Base) limitsPool() *pgxpool.Pool {
	if b.LimitsDB != nil {
		return b.LimitsDB
	}
	return b.DB
}

func (b *Base) guestPool() *pgxpool.Pool {
	if b.GuestDB != nil {
		return b.GuestDB
	}
	return b.DB
}

// ----- Response shaping ------------------------------------------------------

type ListMeta struct {
	HasMore bool   `json:"has_more"`
	Cursor  string `json:"cursor,omitempty"`
	Total   *int64 `json:"total,omitempty"`
}

type listEnvelope[T any] struct {
	Data []T      `json:"data"`
	Meta ListMeta `json:"meta"`
}

func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteList[T any](w http.ResponseWriter, rows []T, meta ListMeta) {
	if rows == nil {
		rows = []T{}
	}
	WriteJSON(w, http.StatusOK, listEnvelope[T]{Data: rows, Meta: meta})
}

// WriteErr is kept for backwards-compat with non-request-aware call sites.
// Prefer Fail(w, r, status, code, msg) for new code — it includes trace_id.
func WriteErr(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// ----- Cursor pagination -----------------------------------------------------
// Cursor = base64url(JSON {t: RFC3339Nano, i: id})
// Keyset is ordered by (created_at DESC, id DESC). Stable on ties.

type Cursor struct {
	T string `json:"t"`
	I string `json:"i"`
}

func EncodeCursor(t time.Time, id string) string {
	b, _ := json.Marshal(Cursor{T: t.UTC().Format(time.RFC3339Nano), I: id})
	return base64.RawURLEncoding.EncodeToString(b)
}

func DecodeCursor(s string) (time.Time, string, error) {
	if s == "" {
		return time.Time{}, "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("bad cursor")
	}
	var c Cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return time.Time{}, "", fmt.Errorf("bad cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, c.T)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("bad cursor")
	}
	return t, c.I, nil
}

// ParseLimit clamps ?limit= to [1, 200] with a default of 50.
func ParseLimit(r *http.Request, def, max int) int {
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			if n < 1 {
				return 1
			}
			if n > max {
				return max
			}
			return n
		}
	}
	return def
}

// ----- Body decode -----------------------------------------------------------

func DecodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("empty body")
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// ----- DB error sniffing ----------------------------------------------------

func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// ----- Context helper to pass the request's Ctx with a reasonable DB budget.
func DBCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}
