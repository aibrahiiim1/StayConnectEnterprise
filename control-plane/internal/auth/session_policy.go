package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A console session lives only as long as the access it was granted for. The session is a bearer token, so every
// request re-reads the operator from the database and ends the session when:
//
//	the operator row is gone or not active
//	the session predates sessions_not_before (set on disable, role change, password reset, removal)
//	the session is older than the absolute cap (central_security_settings.operator_session_max_hours)
//	there is no activity for longer than the idle limit (operator_idle_minutes); the background whoami poll is not activity
//
// The request is authorised with the current roles, never the snapshot stored in the session.

// Defaults and bounds of the absolute lifetime (hours) and idle limit (minutes).
const (
	DefaultSessionMaxHours = 12
	MinSessionMaxHours     = 1
	MaxSessionMaxHours     = 168
	DefaultIdleMinutes     = 30
	MinIdleMinutes         = 5
	MaxIdleMinutes         = 240
)

var ErrSessionEnded = errors.New("session ended")

type OperatorState struct {
	Found             bool
	Status            string
	Email             string
	DisplayName       string
	SessionsNotBefore *time.Time
	Roles             []string
	DefaultTenant     string
	MaxHours          int
	IdleMinutes       int
}

// Validate applies the policy above, returning the session with the operator's current identity and roles, or ErrSessionEnded.
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
	idle := st.IdleMinutes
	if idle < MinIdleMinutes || idle > MaxIdleMinutes {
		idle = DefaultIdleMinutes
	}
	if now.Sub(s.LastSeenAt) > time.Duration(idle)*time.Minute {
		return nil, "signed out after inactivity", ErrSessionEnded
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
	limit := s.CreatedAt.Add(time.Duration(maxHours) * time.Hour)
	if out.ExpiresAt.After(limit) {
		out.ExpiresAt = limit
	}
	out.AbsoluteExpiresAt = limit
	out.IdleExpiresAt = s.LastSeenAt.Add(time.Duration(idle) * time.Minute)
	return &out, "", nil
}

type SessionChecker interface {
	OperatorState(ctx context.Context, operatorID string) (OperatorState, error)
}

type DBSessionChecker struct{ DB *pgxpool.Pool }

func (c DBSessionChecker) OperatorState(ctx context.Context, operatorID string) (OperatorState, error) {
	var st OperatorState
	var roles, tenants []string
	err := c.DB.QueryRow(ctx, `
        SELECT o.status, o.email, COALESCE(o.display_name,''), o.sessions_not_before,
               COALESCE(array_agg(r.role ORDER BY r.role) FILTER (WHERE r.role IS NOT NULL), '{}'),
               COALESCE(array_agg(COALESCE(r.tenant_id::text,'') ORDER BY r.role) FILTER (WHERE r.role IS NOT NULL), '{}'),
               COALESCE((SELECT operator_session_max_hours FROM central_security_settings WHERE id), $2),
               COALESCE((SELECT operator_idle_minutes FROM central_security_settings WHERE id), $3)
          FROM operators o
          LEFT JOIN operator_roles r ON r.operator_id = o.id
         WHERE o.id::text = $1
         GROUP BY o.id`, operatorID, DefaultSessionMaxHours, DefaultIdleMinutes).
		Scan(&st.Status, &st.Email, &st.DisplayName, &st.SessionsNotBefore, &roles, &tenants, &st.MaxHours, &st.IdleMinutes)
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

// EndOperatorSessions makes every existing session of the operator unusable from the next request; call it inside
// the transaction that disables, demotes or re-passwords them. It uses the clock that stamps Session.CreatedAt, so
// a later sign-in is never mistaken for an earlier one.
func EndOperatorSessions(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, operatorID string) error {
	_, err := q.Exec(ctx, `UPDATE operators SET sessions_not_before = $2 WHERE id::text = $1`, operatorID, time.Now())
	return err
}
