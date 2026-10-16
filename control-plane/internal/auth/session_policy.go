package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A CONSOLE SESSION LIVES ONLY AS LONG AS THE ACCESS IT WAS GRANTED FOR.
//
// The Redis session is a bearer token with a 12-hour SLIDING lifetime, and it used to carry the operator's
// roles as they were at sign-in. So a disabled, demoted or removed operator kept working until they stopped
// using the console for 12 hours. Now every request re-reads the operator from the database:
//
//	the operator row is gone or not active            -> session ended
//	the session was created before sessions_not_before -> session ended (set on disable, role change,
//	                                                      password reset, removal)
//	the session is older than the absolute cap          -> session ended, however active it was
//	                                                      (central_security_settings.operator_session_max_hours)
//
// and the roles the request is authorised with are the CURRENT roles, never the snapshot.

// Session limits for the absolute lifetime setting.
const (
	DefaultSessionMaxHours = 24
	MinSessionMaxHours     = 1
	MaxSessionMaxHours     = 168
)

// ErrSessionEnded means the session must not be used again; the middleware destroys it.
var ErrSessionEnded = errors.New("session ended")

// OperatorState is what the database says about the session's operator right now.
type OperatorState struct {
	Found             bool
	Status            string
	Email             string
	DisplayName       string
	SessionsNotBefore *time.Time
	Roles             []string
	DefaultTenant     string
	MaxHours          int
}

// Validate applies the policy above. It returns the session refreshed with the operator's current identity
// and roles, or ErrSessionEnded with the reason.
func Validate(s *Session, st OperatorState, now time.Time) (*Session, string, error) {
	switch {
	case !st.Found:
		return nil, "operator removed", ErrSessionEnded
	case st.Status != "active":
		return nil, "operator " + st.Status, ErrSessionEnded
	case st.SessionsNotBefore != nil && s.CreatedAt.Before(*st.SessionsNotBefore):
		return nil, "access changed after sign-in", ErrSessionEnded
	}
	maxHours := st.MaxHours
	if maxHours < MinSessionMaxHours || maxHours > MaxSessionMaxHours {
		maxHours = DefaultSessionMaxHours
	}
	if now.Sub(s.CreatedAt) > time.Duration(maxHours)*time.Hour {
		return nil, "session lifetime reached", ErrSessionEnded
	}
	out := *s
	out.Email, out.DisplayName = st.Email, st.DisplayName
	out.Roles = append([]string{}, st.Roles...)
	out.IsSuperAdmin = false
	for _, r := range out.Roles {
		if r == "platform_owner" || r == "platform_admin" {
			out.IsSuperAdmin = true
		}
	}
	out.DefaultTenantID = st.DefaultTenant
	if limit := s.CreatedAt.Add(time.Duration(maxHours) * time.Hour); out.ExpiresAt.After(limit) {
		out.ExpiresAt = limit
	}
	return &out, "", nil
}

// SessionChecker loads OperatorState.
type SessionChecker interface {
	OperatorState(ctx context.Context, operatorID string) (OperatorState, error)
}

// DBSessionChecker reads OperatorState from Central's database.
type DBSessionChecker struct{ DB *pgxpool.Pool }

// OperatorState reads the operator, their roles and the absolute session cap in one round trip.
func (c DBSessionChecker) OperatorState(ctx context.Context, operatorID string) (OperatorState, error) {
	var st OperatorState
	var roles, tenants []string
	err := c.DB.QueryRow(ctx, `
        SELECT o.status, o.email, COALESCE(o.display_name,''), o.sessions_not_before,
               COALESCE(array_agg(r.role ORDER BY r.role) FILTER (WHERE r.role IS NOT NULL), '{}'),
               COALESCE(array_agg(COALESCE(r.tenant_id::text,'') ORDER BY r.role) FILTER (WHERE r.role IS NOT NULL), '{}'),
               COALESCE((SELECT operator_session_max_hours FROM central_security_settings WHERE id), $2)
          FROM operators o
          LEFT JOIN operator_roles r ON r.operator_id = o.id
         WHERE o.id::text = $1
         GROUP BY o.id`, operatorID, DefaultSessionMaxHours).
		Scan(&st.Status, &st.Email, &st.DisplayName, &st.SessionsNotBefore, &roles, &tenants, &st.MaxHours)
	if errors.Is(err, pgx.ErrNoRows) {
		return OperatorState{}, nil
	}
	if err != nil {
		return OperatorState{}, err
	}
	st.Found = true
	st.Roles = roles
	for _, t := range tenants {
		if t != "" {
			st.DefaultTenant = t
			break
		}
	}
	return st, nil
}

// EndOperatorSessions makes every existing console session of the operator unusable from the next request
// on. Called inside the transaction that disables, demotes or re-passwords them. The instant comes from the
// same clock that stamps Session.CreatedAt, so a sign-in after the change is never mistaken for one before it.
func EndOperatorSessions(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, operatorID string) error {
	_, err := q.Exec(ctx, `UPDATE operators SET sessions_not_before = $2 WHERE id::text = $1`, operatorID, time.Now())
	return err
}
