package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters match the cloud control plane.
const (
	argonMemory  = 64 * 1024
	argonTime    = 1
	argonThreads = 4
	argonSaltLen = 16
	argonKeyLen  = 32
)

func hashPassword(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(pw, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// Sessions live in edged's memory (one edged per appliance; re-login after a restart is acceptable). The cookie
// name differs from the cloud's sc_session on purpose.
const sessionCookie = "sc_edge_session"

type session struct {
	OperatorID  string
	Email       string
	DisplayName string
	Roles       []string
	Expires     time.Time
	// Session limits are measured from these: LastSeen moves with activity, CreatedAt never does.
	CreatedAt time.Time
	LastSeen  time.Time
	// MustChangePassword confines the session to changing the password; requireAuth refuses every other route.
	MustChangePassword bool
}

type sessionStore struct {
	mu  sync.Mutex
	m   map[string]*session
	ttl time.Duration
}

func newSessionStore(ttl time.Duration) *sessionStore {
	s := &sessionStore{m: map[string]*session{}, ttl: ttl}
	go func() {
		for range time.Tick(10 * time.Minute) {
			now := time.Now()
			s.mu.Lock()
			for k, v := range s.m {
				if now.After(v.Expires) {
					delete(s.m, k)
				}
			}
			s.mu.Unlock()
		}
	}()
	return s
}

// sessionStoreCap is the longest session the setting allows; active() enforces the idle and absolute limits.
const sessionStoreCap = 24 * time.Hour

func (s *sessionStore) create(sess *session) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	now := time.Now()
	sess.Expires = now.Add(s.ttl)
	// A replacement session keeps the original sign-in time so a password change cannot extend the absolute limit.
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}
	sess.LastSeen = now
	s.mu.Lock()
	s.m[tok] = sess
	s.mu.Unlock()
	return tok
}

func (s *sessionStore) get(tok string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[tok]
	if !ok || time.Now().After(sess.Expires) {
		delete(s.m, tok)
		return nil
	}
	sess.Expires = time.Now().Add(s.ttl) // sliding
	return sess
}

// active returns the session if within the idle and maxAge limits (ending it otherwise); touch says whether
// this request counts as activity.
func (s *sessionStore) active(tok string, idle, maxAge time.Duration, touch bool) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[tok]
	now := time.Now()
	if !ok || now.After(sess.Expires) || now.Sub(sess.LastSeen) > idle || now.Sub(sess.CreatedAt) > maxAge {
		delete(s.m, tok)
		return nil
	}
	if touch {
		sess.LastSeen = now
	}
	return sess
}

// peek does not slide the expiry: for callers that only want to know who is asking.
func (s *sessionStore) peek(tok string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[tok]
	if !ok || time.Now().After(sess.Expires) {
		return nil
	}
	return sess
}

func (s *sessionStore) destroy(tok string) {
	s.mu.Lock()
	delete(s.m, tok)
	s.mu.Unlock()
}

func (s *sessionStore) destroyOperator(operatorID string) {
	s.mu.Lock()
	for k, v := range s.m {
		if v.OperatorID == operatorID {
			delete(s.m, k)
		}
	}
	s.mu.Unlock()
}

type perm int

const (
	permNone perm = iota
	permRead
	permWrite
)

// rolePerms maps role -> resource -> permission; site_admin implicitly has write everywhere
// (docs/ROLE_AND_SCOPE_MATRIX.md).
//
// Guest sign-in is split across separate keys because the model has one axis (read/write) and the distinctions
// are between kinds of read or between blast radii; separate resources keep the boundary server-enforced:
//
//	guest-signin-attempts     the list, rooms, results, diagnostic reasons
//	guest-signin-credentials  the submitted and accepted values, unmasked
//	guest-signin-protection   WRITE changes the property's thresholds (affects every guest)
//	guest-signin-restrictions WRITE ends ONE device's wait early
//	code-requests             READ lists code requests; WRITE lets one device try again
//	code-reveal               WRITE reads a LIVE one-time code, under password step-up
//
// No key implies another: an operator who can end a restriction cannot read what anyone typed or weaken the
// control, and clearing a device's history is allowed where vouching for an address by revealing a code is not.
var rolePerms = map[string]map[string]perm{
	"hotel_it_manager": {
		"code-requests": permRead, "code-reveal": permWrite,
		"guest-accounts": permWrite,
		"sessions":       permWrite,
		"usage":          permRead,
		"auth-methods":   permWrite, "walled-garden": permWrite, "client-groups": permWrite,
		"portal-branding": permWrite, "portal-assets": permWrite, "notification-providers": permWrite,
		"social-providers":  permWrite,
		"payment-providers": permWrite,
		// Switching a module and approving a PMS interface's financial onboarding are site_admin decisions.
		"modules": permRead, "pms-financial-onboarding": permRead,
		"network": permWrite,
		// Portal address and hotel portal domain are network configuration, which this role owns; every change
		// also needs the password step-up.
		"portal-https":        permWrite,
		"commercial-packages": permWrite,
		// This role owns the PMS integration: stays/events/resolutions are read evidence.
		"pms-stays": permRead, "pms-events": permRead, "pms-resolutions": permRead,
		"checkout-grace": permWrite, "operational-alerts": permWrite,
		"pms-event-decisions": permWrite,
		// Guest names and reservation numbers are, with the room, the sign-in credential: a field-level key, not a route.
		"pms-stay-identity": permRead,
		// Routing and source conflicts are read-only everywhere (topology decision / evidence, not a control).
		"pms-interfaces": permWrite, "pms-routing": permRead, "pms-source-conflicts": permRead,
		// Reset and revoke, under step-up and a reason; no surface ever returns a stored PIN.
		"post-stay-profiles": permWrite,
		"stay-transfers":     permWrite,
		// Sees the financial review queue as integration evidence; the decision belongs to payments_operator.
		"financial-review": permRead,
		// financial-ops mirrors financial-review so the two cannot drift apart.
		"financial-ops": permRead,
		"operators":     permRead, "audit": permRead,
		"reports": permRead, "backups": permRead, "license": permRead,
		// Database dump download: IT manager and site_admin only, behind password step-up.
		"backup-download": permWrite,
		// Guest data export and erasure is a data-protection act, not a desk one: this role and site_admin only,
		// behind step-up and a reason.
		"guest-data-rights":         permWrite,
		"guest-device-self-service": permWrite,
		"diagnostics":               permWrite,
		// The IT manager owns the PMS integration, and "which guests cannot sign in, and why" is its most direct symptom.
		"guest-signin-attempts": permRead, "guest-signin-credentials": permWrite,
		// The protection policy is property-wide configuration; whoever sets it may also waive one restriction.
		"guest-signin-protection": permWrite, "guest-signin-restrictions": permWrite,
		// The case list is read-only for every role: checkout boundaries come from APPLIED GO events, not the desk.
		"pms-reconciliation": permRead,
		// Closing the stays a complete roster omits is an integration action, too heavy for a desk under pressure.
		"pms-roster-reconciliation": permWrite,
		"vouchers":                  permWrite,
		"voucher-code-settings":     permWrite,
		"account-password-settings": permWrite,
		// Deliberately no voucher-codes: neither configuration nor the PMS integration requires reading a code in the clear.
	},
	"front_office_operator": {
		"code-requests": permRead, "code-reveal": permWrite,
		// The reception desk answers the guest in front of it, so it holds the guest's sign-in credentials.
		"guest-signin-attempts": permRead, "guest-signin-credentials": permWrite,
		// The desk may end one device's wait but not re-tune the policy: it must not be able to turn "five" into
		// "twenty" for everybody to help one person.
		"guest-signin-restrictions": permWrite, "guest-signin-protection": permRead,
		"guest-accounts": permWrite, "sessions": permWrite,
		"usage": permRead,
		// Reads stays/events and triages alerts, but never edits the grace policy.
		"pms-stays": permRead, "pms-events": permRead, "operational-alerts": permWrite, "checkout-grace": permRead,
		// The desk identifies the guest in front of it by name and reservation.
		"pms-stay-identity": permRead,
		// Read-only on the integration: it may see whether the PMS is reachable but not publish or rotate.
		"pms-interfaces": permRead, "pms-routing": permRead, "pms-source-conflicts": permRead,
		// Closing stays in bulk is an integration action, not a desk one.
		"pms-reconciliation": permRead, "pms-roster-reconciliation": permRead,
		// A guest who lost their post-stay PIN turns up here; reset and revoke need step-up and a reason.
		"post-stay-profiles": permWrite,
		"stay-transfers":     permWrite,
		"financial-review":   permRead,
		"financial-ops":      permRead,
		"auth-methods":       permRead, "walled-garden": permRead, "client-groups": permRead, "reports": permRead, "audit": permRead, "license": permRead, "backups": permRead,
		"guest-device-self-service": permRead,
		"diagnostics":               permRead,
		// Reading an issued code (a smudged card at the desk) is gated by step-up and an audit row.
		"vouchers": permWrite, "voucher-codes": permWrite,
		"voucher-code-settings": permRead, "account-password-settings": permRead,
	},
	"guest_relations_operator": {
		"code-requests":         permRead,
		"guest-signin-attempts": permRead, "guest-signin-credentials": permWrite,
		"guest-signin-restrictions": permWrite, "guest-signin-protection": permRead,
		"guest-accounts": permWrite, "sessions": permWrite, "usage": permRead,
		"pms-stays": permRead, "pms-events": permRead, "operational-alerts": permWrite, "checkout-grace": permRead,
		// This role already reads guest-signin-credentials (names included), so hiding names on Stays protects nothing.
		"pms-stay-identity": permRead,
		"pms-interfaces":    permRead, "pms-routing": permRead, "pms-source-conflicts": permRead,
		"pms-reconciliation": permRead, "pms-roster-reconciliation": permRead,
		"post-stay-profiles": permWrite,
		"stay-transfers":     permWrite,
		"auth-methods":       permRead, "reports": permRead, "client-groups": permRead,
		"audit": permRead, "license": permRead, "backups": permRead, "walled-garden": permRead,
		"guest-device-self-service": permRead,
		"diagnostics":               permRead,
		"vouchers":                  permWrite, "voucher-codes": permWrite, "voucher-code-settings": permRead,
		"account-password-settings": permRead,
	},
	"voucher_operator": {
		"guest-accounts": permWrite, "sessions": permRead, "reports": permRead,
		"license": permRead, "diagnostics": permRead,
		"vouchers": permWrite, "voucher-codes": permWrite,
		// Reads the format so the print station knows what it prints; does not set it.
		"voucher-code-settings": permRead, "account-password-settings": permRead,
	},
	"payments_operator": {
		"payment-providers": permRead, "modules": permRead, "pms-financial-onboarding": permRead,
		"guest-device-self-service": permRead,
		// Holds financial-review WRITE; every action also needs password re-authentication at the route.
		"financial-review": permWrite,
		"financial-ops":    permWrite,
		"sessions":         permRead, "reports": permRead, "audit": permRead, "license": permRead,
		"diagnostics": permRead,
		// Read-only on everything that is not money.
		"usage": permRead, "commercial-packages": permRead,
		// A voucher is a commercial instrument: this role reads the list, reads no code and cancels nothing.
		"vouchers": permRead,
	},
	"site_viewer": {
		"code-requests": permRead,
		// The attempts list but not the credentials: a read-only observer has no reason to hold what every guest typed.
		"guest-signin-attempts":   permRead,
		"guest-signin-protection": permRead, "guest-signin-restrictions": permRead,
		"guest-accounts": permRead, "sessions": permRead, "usage": permRead, "auth-methods": permRead,
		"walled-garden": permRead, "client-groups": permRead, "portal-branding": permRead, "portal-assets": permRead, "notification-providers": permRead, "social-providers": permRead,
		"payment-providers": permRead, "modules": permRead, "audit": permRead, "reports": permRead,
		"backups": permRead, "license": permRead, "network": permRead, "diagnostics": permRead,
		"portal-https":        permRead,
		"commercial-packages": permRead,
		// Deliberately no pms-stay-identity: names and reservation numbers are, with the room, the sign-in credential.
		"pms-stays": permRead, "pms-events": permRead, "pms-resolutions": permRead,
		"pms-reconciliation": permRead,

		"checkout-grace": permRead, "operational-alerts": permRead,
		"pms-interfaces": permRead, "pms-routing": permRead, "pms-source-conflicts": permRead,
		"post-stay-profiles":        permRead,
		"guest-device-self-service": permRead,
		"stay-transfers":            permRead,
		"financial-review":          permRead,
		"financial-ops":             permRead,
		// Sees which cards exist, reads no code (as with guest-signin-credentials).
		"vouchers": permRead, "voucher-code-settings": permRead, "account-password-settings": permRead,
	},
	// Legacy tenant roles accepted for migrated operators.
	"tenant_admin":    nil, // treated like site_admin below
	"tenant_operator": nil, // treated like hotel_it_manager below
}

func permFor(roles []string, resource string, want perm) bool {
	for _, role := range roles {
		switch role {
		case "site_admin", "tenant_admin":
			return true
		case "tenant_operator":
			role = "hotel_it_manager"
		}
		if p, ok := rolePerms[role][resource]; ok && p >= want {
			return true
		}
	}
	return false
}

type ctxKey int

const sessKey ctxKey = 1

var sessPolicyKey = struct{ name string }{"session-policy"}

func (s *server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ck, err := r.Cookie(sessionCookie)
		if err != nil || ck.Value == "" {
			jsonErr(w, http.StatusUnauthorized, "unauthenticated", "login required")
			return
		}
		// The console's background whoami check is not activity; counting it would keep open tabs signed in forever.
		pol := s.sessionPolicyFor(r.Context())
		touch := isActivity(r)
		sess := s.sessions.active(ck.Value, pol.idle(), pol.max(), touch)
		if sess == nil {
			jsonErr(w, http.StatusUnauthorized, "unauthenticated", "session expired")
			return
		}
		// Enforced here for every route, so nothing is reachable by URL or API before the password is replaced.
		if sess.MustChangePassword && !mustChangeAllowed(r) {
			jsonErr(w, http.StatusForbidden, "password_change_required",
				"Set a new password before using the Admin Console.")
			return
		}
		ctx := context.WithValue(r.Context(), sessKey, sess)
		ctx = context.WithValue(ctx, sessPolicyKey, pol)
		ctx = context.WithValue(ctx, reauthNoteKey, &reauthNote{})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// backgroundHeader marks console-initiated requests (polls, timers, whoami), which must not count as activity or
// an open tab would never time out.
const backgroundHeader = "X-OneGate-Background"

// isActivity: background requests and whoami do not extend the idle limit; keepalive always does.
func isActivity(r *http.Request) bool {
	switch strings.TrimSuffix(r.URL.Path, "/") {
	case "/edge/v1/auth/keepalive":
		return true
	case "/edge/v1/auth/whoami":
		return false
	}
	return r.Header.Get(backgroundHeader) == ""
}

func sessFrom(ctx context.Context) *session {
	s, _ := ctx.Value(sessKey).(*session)
	return s
}

func (s *server) resourcePermission(resource string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess := sessFrom(r.Context())
			if sess == nil {
				jsonErr(w, http.StatusUnauthorized, "unauthenticated", "login required")
				return
			}
			want := permRead
			switch r.Method {
			case http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
				want = permWrite
			}
			if !permFor(sess.Roles, resource, want) {
				jsonErr(w, http.StatusForbidden, "forbidden", "role lacks access to "+resource)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *server) requireRole(resource string, want perm) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess := sessFrom(r.Context())
			if sess == nil || !permFor(sess.Roles, resource, want) {
				jsonErr(w, http.StatusForbidden, "forbidden", "insufficient role")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *server) loadRoles(ctx context.Context, operatorID string) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT role FROM operator_roles WHERE operator_id = $1 ORDER BY role`, operatorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		out = append(out, role)
	}
	return out, rows.Err()
}

// sessionEnds is the earlier of the idle and absolute limits.
func sessionEnds(sess *session, pol sessionPolicy) time.Time {
	ends := sess.LastSeen.Add(pol.idle())
	if abs := sess.CreatedAt.Add(pol.max()); abs.Before(ends) {
		ends = abs
	}
	return ends
}

func sessionPolicyFrom(ctx context.Context) sessionPolicy {
	if pol, ok := ctx.Value(sessPolicyKey).(sessionPolicy); ok {
		return pol
	}
	return defaultSessionPolicy
}

func (s *server) whoami(w http.ResponseWriter, r *http.Request) {
	sess := sessFrom(r.Context())
	pol := sessionPolicyFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"operator_id":          sess.OperatorID,
		"email":                sess.Email,
		"display_name":         sess.DisplayName,
		"roles":                sess.Roles,
		"site_id":              s.siteID,
		"expires_at":           sessionEnds(sess, pol).UTC(),
		"idle_timeout_minutes": pol.IdleTimeoutMinutes,
		"must_change_password": sess.MustChangePassword,
	})
}

// keepalive backs the idle warning's "Stay signed in" (WCAG 2.2.1): passing requireAuth is the activity. It
// cannot move the absolute limit.
func (s *server) keepalive(w http.ResponseWriter, r *http.Request) {
	sess := sessFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"expires_at": sessionEnds(sess, sessionPolicyFrom(r.Context())).UTC(),
	})
}

var errNotFound = errors.New("not found")
