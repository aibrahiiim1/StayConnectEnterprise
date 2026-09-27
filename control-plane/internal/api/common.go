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

	// Redis backs step-up re-authentication checks (RequireReauth) and rate limits.
	Redis *redis.Client

	// AssignKey is the dedicated Ed25519 key that signs appliance assignment documents. nil disables
	// activation, moves and retirement (nothing is ever assigned without a signed document).
	AssignKey ed25519.PrivateKey

	// CA issues appliance client certificates; nil disables certificate issuance.
	CA          *pki.CA
	ClientValid time.Duration      // issued client-cert lifetime
	Lic         *licensing.Service // hardware-bound licence issuance; nil when no vendor key
}

// issueAssignment signs + persists a new current assignment for the appliance (bumping its version).
func (b *Base) issueAssignment(ctx context.Context, applianceID, state string) error {
	if b.AssignKey == nil {
		return errors.New("assignment signing key not configured")
	}
	ab := &AssignmentBase{Base: b, SignKey: b.AssignKey}
	_, err := ab.Issue(ctx, applianceID, state)
	return err
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

// AssignmentBaseFrom exposes the assignment signer to other API bases that must mint a signed assignment as
// part of a larger operation — offline first activation, which has to put one inside its package. It returns
// nil when no assignment key is configured, so callers disable the feature rather than emit an unsigned one.
func AssignmentBaseFrom(b *Base) *AssignmentBase {
	if b == nil || b.AssignKey == nil {
		return nil
	}
	return &AssignmentBase{Base: b, SignKey: b.AssignKey}
}
