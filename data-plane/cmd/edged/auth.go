package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// ----- argon2id passwords (same parameters as the cloud control plane) ------

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

// ----- in-memory session store ------------------------------------------------

// Hotel Admin sessions live in edged's memory: an appliance runs exactly one
// edged, and re-login after a daemon restart is acceptable for a management
// UI. Cookie name intentionally differs from the cloud's sc_session.
const sessionCookie = "sc_edge_session"

type session struct {
	OperatorID  string
	Email       string
	DisplayName string
	Roles       []string
	Expires     time.Time
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

func (s *sessionStore) create(sess *session) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	sess.Expires = time.Now().Add(s.ttl)
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

func (s *sessionStore) destroy(tok string) {
	s.mu.Lock()
	delete(s.m, tok)
	s.mu.Unlock()
}

// destroyOperator kills every session belonging to an operator (disable).
func (s *sessionStore) destroyOperator(operatorID string) {
	s.mu.Lock()
	for k, v := range s.m {
		if v.OperatorID == operatorID {
			delete(s.m, k)
		}
	}
	s.mu.Unlock()
}

// ----- role → permission matrix -------------------------------------------------

type perm int

const (
	permNone perm = iota
	permRead
	permWrite
)

// rolePerms maps site role → resource → permission. site_admin implicitly
// has permWrite everywhere. Matrix per docs/ROLE_AND_SCOPE_MATRIX.md.
var rolePerms = map[string]map[string]perm{
	"hotel_it_manager": {
		"guest-accounts": permWrite,
		"sessions":       permWrite,
		"auth-methods":   permWrite, "walled-garden": permWrite,
		"portal-branding": permWrite, "notification-providers": permWrite,
		"social-providers": permWrite, "stripe-accounts": permWrite,
		"network": permWrite,
		// Phase 2 (DARK) commercial packages: revisioned CRUD is a manager action.
		"commercial-packages": permWrite,
		// Phase 3 (DARK): the IT manager owns the PMS integration, so publishing the checkout-grace policy
		// and clearing operational alerts are manager actions; stays/events/resolutions are read evidence.
		"pms-stays": permRead, "pms-events": permRead, "pms-resolutions": permRead,
		"checkout-grace": permWrite, "operational-alerts": permWrite,
		// The interface itself: publishing a Revision and rotating a credential are the two actions this
		// role exists for. Routing and source conflicts are read-only everywhere — the first is a network
		// topology decision made where the networks are configured, the second is evidence, not a control.
		"pms-interfaces": permWrite, "pms-routing": permRead, "pms-source-conflicts": permRead,
		// Phase 5 (DARK): post-stay identities. WRITE here means reset (rotate the credential) and revoke
		// (end post-stay access for that stay episode) -- both additionally require password step-up and a
		// bounded reason at the route. It is never a way to READ a PIN: no surface returns a stored one.
		"post-stay-profiles": permWrite,
		// Cross-PMS transfer: the IT manager owns the integration, and a transfer is an integration action as
		// much as a guest one. WRITE means preview and execute, both under step-up and a bounded reason.
		"stay-transfers": permWrite,
		// The IT manager can SEE the financial review queue as integration evidence, and cannot decide it:
		// section 15 gives the decision to payments_operator (and site_admin).
		"financial-review": permRead,
		// financial-ops mirrors financial-review deliberately: it is the same readership looking at the
		// same money from a different angle, and two permissions for one boundary is how they drift apart.
		"financial-ops": permRead,
		"operators":     permRead, "audit": permRead,
		"reports": permRead, "backups": permRead, "license": permRead,
		// Phase 6 (DARK): the per-appliance Guest Device Self-Service setting. WRITE belongs here for the
		// same reason auth-methods does -- it decides which capabilities the property offers its guests,
		// which is a configuration decision rather than a daily desk action. No new ROLE is introduced: the
		// existing matrix already expresses this ownership.
		"guest-device-self-service": permWrite,
		// Health & diagnostics: managers may run Recheck/Restart (write, step-up).
		"diagnostics": permWrite,
	},
	"front_office_operator": {
		"guest-accounts": permWrite, "sessions": permWrite,
		// Phase 3 (DARK): front desk reads stays/events and triages alerts, but never edits the grace policy.
		"pms-stays": permRead, "pms-events": permRead, "operational-alerts": permWrite, "checkout-grace": permRead,
		// Read-only on the integration: the front desk needs to see whether the PMS is reachable before
		// telling a guest to try again, but must not be able to publish or rotate anything.
		"pms-interfaces": permRead, "pms-routing": permRead, "pms-source-conflicts": permRead,
		// Phase 5 (DARK): the front desk is where a guest who lost their post-stay PIN actually turns up,
		// so this role can rotate and revoke -- under step-up, a mandatory reason and audit like everyone
		// else. Reset is a rotation; revoke is terminal for the episode.
		"post-stay-profiles": permWrite,
		// The front desk is where a guest moving between the group's properties actually presents.
		"stay-transfers":   permWrite,
		"financial-review": permRead,
		"financial-ops":    permRead,
		"auth-methods":     permRead, "walled-garden": permRead, "reports": permRead, "audit": permRead, "license": permRead, "backups": permRead,
		// Phase 6 (DARK): the desk may SEE whether the property offers guest device self-service -- it is
		// the answer to "why can't I remove my old phone" -- but changing a property capability is not a
		// desk action.
		"guest-device-self-service": permRead,
		"diagnostics":               permRead,
	},
	"guest_relations_operator": {
		"guest-accounts": permWrite, "sessions": permWrite,
		"pms-stays": permRead, "pms-events": permRead, "operational-alerts": permWrite, "checkout-grace": permRead,
		// Read-only on the integration: the front desk needs to see whether the PMS is reachable before
		// telling a guest to try again, but must not be able to publish or rotate anything.
		"pms-interfaces": permRead, "pms-routing": permRead, "pms-source-conflicts": permRead,
		"post-stay-profiles": permWrite,
		"stay-transfers":     permWrite,
		"auth-methods":       permRead, "reports": permRead,
		"audit": permRead, "license": permRead, "backups": permRead, "walled-garden": permRead,
		// Phase 6 (DARK): same readership as the front desk, for the same reason -- guest relations answers
		// the same question and changes no property capability.
		"guest-device-self-service": permRead,
		"diagnostics":               permRead,
	},
	"voucher_operator": {
		"guest-accounts": permWrite, "sessions": permRead, "reports": permRead,
		"license": permRead, "diagnostics": permRead,
	},
	"payments_operator": {
		"stripe-accounts": permRead,
		// Phase 6 (DARK): read-only, which is this role's established relationship with everything that is
		// not money. It is on the list rather than absent because "read-only on everything else" is the
		// documented semantic, and silently omitting a resource would narrow the role by accident.
		"guest-device-self-service": permRead,
		// Contract section 15: payments_operator is the role that holds financial-review WRITE. Every
		// action additionally requires password re-authentication, enforced at the route.
		"financial-review": permWrite,
		"financial-ops":    permWrite,
		"sessions":         permRead, "reports": permRead, "audit": permRead, "license": permRead,
		"diagnostics": permRead,
	},
	"site_viewer": {
		"guest-accounts": permRead, "sessions": permRead, "auth-methods": permRead,
		"walled-garden": permRead, "portal-branding": permRead, "notification-providers": permRead, "social-providers": permRead,
		"stripe-accounts": permRead, "audit": permRead, "reports": permRead,
		"backups": permRead, "license": permRead, "network": permRead, "diagnostics": permRead,
		"commercial-packages": permRead,
		// Phase 3 (DARK): a viewer sees the evidence and never acts on it.
		"pms-stays": permRead, "pms-events": permRead, "pms-resolutions": permRead,
		"checkout-grace": permRead, "operational-alerts": permRead,
		"pms-interfaces": permRead, "pms-routing": permRead, "pms-source-conflicts": permRead,
		// Phase 5 (DARK): a viewer sees WHETHER a post-stay identity can authenticate, and never acts on it.
		"post-stay-profiles": permRead,
		// Phase 6 (DARK): a viewer sees whether the property offers guest device self-service, and cannot
		// change it -- read-only everywhere is the whole definition of this role.
		"guest-device-self-service": permRead,
		// A viewer reads the lineage and the review signals, and acts on neither.
		"stay-transfers":   permRead,
		"financial-review": permRead,
		// financial-ops mirrors financial-review deliberately: it is the same readership looking at the
		// same money from a different angle, and two permissions for one boundary is how they drift apart.
		"financial-ops": permRead,
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

// ----- middleware -----------------------------------------------------------------

type ctxKey int

const sessKey ctxKey = 1

func (s *server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ck, err := r.Cookie(sessionCookie)
		if err != nil || ck.Value == "" {
			jsonErr(w, http.StatusUnauthorized, "unauthenticated", "login required")
			return
		}
		sess := s.sessions.get(ck.Value)
		if sess == nil {
			jsonErr(w, http.StatusUnauthorized, "unauthenticated", "session expired")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessKey, sess)))
	})
}

func sessFrom(ctx context.Context) *session {
	s, _ := ctx.Value(sessKey).(*session)
	return s
}

// resourcePermission enforces read/write on a resource by HTTP method.
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

// requireRole is a coarse variant for one-off routes.
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

// ----- login/logout/whoami -----------------------------------------------------

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Email == "" || in.Password == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "email and password required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	var id, display, hash, status string
	err := s.db.QueryRow(ctx, `
        SELECT id, COALESCE(display_name,''), COALESCE(password_hash,''), status
          FROM operators WHERE lower(email) = lower($1)
    `, in.Email).Scan(&id, &display, &hash, &status)
	if err != nil || status != "active" || hash == "" || !verifyPassword(in.Password, hash) {
		// Constant-shape failure; do not reveal which check failed.
		jsonErr(w, http.StatusUnauthorized, "unauthenticated", "invalid credentials")
		return
	}
	roles, err := s.loadRoles(ctx, id)
	if err != nil || len(roles) == 0 {
		jsonErr(w, http.StatusForbidden, "forbidden", "no roles assigned")
		return
	}
	tok := s.sessions.create(&session{OperatorID: id, Email: in.Email, DisplayName: display, Roles: roles})
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.secure,
		Expires: time.Now().Add(12 * time.Hour),
	})
	s.audit(r, "operator.login", "operator", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"operator_id": id, "roles": roles})
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

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.destroy(ck.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) whoami(w http.ResponseWriter, r *http.Request) {
	sess := sessFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"operator_id":  sess.OperatorID,
		"email":        sess.Email,
		"display_name": sess.DisplayName,
		"roles":        sess.Roles,
		"site_id":      s.siteID,
		"expires_at":   sess.Expires.UTC(),
	})
}

var errNotFound = errors.New("not found")
