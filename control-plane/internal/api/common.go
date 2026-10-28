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
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	redis "github.com/redis/go-redis/v9"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/licensing"
	"github.com/stayconnect/enterprise/control-plane/internal/pki"
	"github.com/stayconnect/enterprise/control-plane/internal/portaltls"
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

	// PortalTLS owns portal hostnames and certificates; nil only in tools and tests that never retire an appliance.
	PortalTLS *portaltls.Service

	// MFA names an account's sign-in locks so resetting two-step sign-in can lift them; nil falls back to the plain hash.
	MFA *auth.MFA
}

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

// WriteErr is for call sites without a request; prefer Fail, which includes trace_id.
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

// MaxJSONBody bounds every operator request body; nothing the console sends comes near it (audit Phase 1 M9).
const MaxJSONBody = 64 << 10

// fieldLimits are the longest values accepted, by JSON field name, on any operator write (audit Phase 1 M9).
// Names and codes are signed into assignments; the rest are stored and shown to other operators.
var fieldLimits = map[string]int{
	"name":         120,
	"display_name": 120,
	"code":         48,
	"reason":       500,
	"note":         500,
	"confirm":      120,
	"password":     1024,
	"new_password": 1024,
	"email":        254,
}

// FieldLimitError names the field that is too long; its message is safe to show.
type FieldLimitError struct {
	Field string
	Max   int
}

func (e *FieldLimitError) Error() string {
	return fmt.Sprintf("%s must be at most %d characters", e.Field, e.Max)
}

// DecodeJSON decodes a bounded body strictly and then applies fieldLimits.
func DecodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("empty body")
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, MaxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return fmt.Errorf("request body is larger than %d bytes", MaxJSONBody)
		}
		return err
	}
	return CheckFieldLimits(dst)
}

// CheckFieldLimits applies fieldLimits to the string and *string fields of a struct (or pointer to one).
func CheckFieldLimits(v any) error {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		if !rt.Field(i).IsExported() {
			continue
		}
		f := rv.Field(i)
		if f.Kind() == reflect.Pointer && !f.IsNil() {
			f = f.Elem()
		}
		if f.Kind() == reflect.Struct {
			if err := CheckFieldLimits(f.Interface()); err != nil {
				return err
			}
			continue
		}
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if max, ok := fieldLimits[name]; ok && f.Kind() == reflect.String && utf8.RuneCountInString(f.String()) > max {
			return &FieldLimitError{Field: name, Max: max}
		}
	}
	return nil
}

// limitExceeded reports a field or size limit error from DecodeJSON; callers whose body is optional still
// refuse those.
func limitExceeded(w http.ResponseWriter, r *http.Request, err error) bool {
	var fl *FieldLimitError
	if errors.As(err, &fl) || (err != nil && strings.HasPrefix(err.Error(), "request body is larger")) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, badBody(err))
		return true
	}
	return false
}

// badBody is the 400 message for a DecodeJSON error: the field or size limit when that is the cause.
func badBody(err error) string {
	var fl *FieldLimitError
	if errors.As(err, &fl) {
		return fl.Error()
	}
	if err != nil && strings.HasPrefix(err.Error(), "request body is larger") {
		return err.Error()
	}
	return "bad body"
}

func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

func DBCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}

// AssignmentBaseFrom exposes the assignment signer to bases that mint one inside a larger operation (offline
// first activation). It returns nil without an assignment key, so callers disable the feature rather than emit an unsigned one.
func AssignmentBaseFrom(b *Base) *AssignmentBase {
	if b == nil || b.AssignKey == nil {
		return nil
	}
	return &AssignmentBase{Base: b, SignKey: b.AssignKey}
}
