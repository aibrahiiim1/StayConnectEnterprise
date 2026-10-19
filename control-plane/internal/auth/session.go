package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	SessionCookieName = "sc_session"
	sessionKeyPrefix  = "sc:sess:"
	SessionTTL        = 12 * time.Hour
)

type Session struct {
	Token           string    `json:"-"`
	OperatorID      string    `json:"operator_id"`
	Email           string    `json:"email"`
	DisplayName     string    `json:"display_name,omitempty"`
	IsSuperAdmin    bool      `json:"is_super_admin"`              // platform_owner / platform_admin
	DefaultTenantID string    `json:"default_tenant_id,omitempty"` // the customer a customer-scoped user belongs to
	Roles           []string  `json:"roles"`
	CreatedAt       time.Time `json:"created_at"`
	LastSeenAt      time.Time `json:"last_seen_at"`
	ExpiresAt       time.Time `json:"expires_at"`

	// Set per request by Validate from the current settings and never stored: when the session ends if no
	// further activity arrives (idle limit), and when it ends however active it is (absolute lifetime).
	IdleExpiresAt     time.Time `json:"-"`
	AbsoluteExpiresAt time.Time `json:"-"`
}

type SessionStore struct {
	R *redis.Client
}

func key(token string) string { return sessionKeyPrefix + token }

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Create issues a new session and returns the token + Session.
func (s *SessionStore) Create(ctx context.Context, base Session) (*Session, error) {
	tok, err := newToken()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	sess := base
	sess.Token = tok
	sess.CreatedAt = now
	sess.LastSeenAt = now
	sess.ExpiresAt = now.Add(SessionTTL)
	buf, err := json.Marshal(sess)
	if err != nil {
		return nil, err
	}
	if err := s.R.Set(ctx, key(tok), buf, SessionTTL).Err(); err != nil {
		return nil, fmt.Errorf("redis set: %w", err)
	}
	return &sess, nil
}

// Get fetches a session by token. Returns (nil, nil) when not found. It changes nothing: activity is recorded by
// Touch, after the request has passed the session policy (0052), so the idle limit is judged on the last real
// activity rather than on this request.
func (s *SessionStore) Get(ctx context.Context, token string) (*Session, error) {
	raw, err := s.R.Get(ctx, key(token)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, err
	}
	sess.Token = token
	return &sess, nil
}

// Touch records activity on a session: LastSeenAt moves to now (at most once a minute, to keep Redis writes
// bounded) and the Redis TTL slides. The idle and absolute limits are enforced by Validate.
//
// ONLY A SESSION THAT STILL EXISTS IS WRITTEN (SET ... XX). A request that read the session just before a
// sign-out deleted it would otherwise write it back, and the signed-out session would work again for its
// whole lifetime.
func (s *SessionStore) Touch(ctx context.Context, sess *Session) error {
	if time.Since(sess.LastSeenAt) <= time.Minute {
		return nil
	}
	stored := *sess
	stored.LastSeenAt = time.Now()
	stored.ExpiresAt = stored.LastSeenAt.Add(SessionTTL)
	buf, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	err = s.R.SetXX(ctx, key(sess.Token), buf, SessionTTL).Err()
	if err == redis.Nil { // gone meanwhile: nothing to touch
		return nil
	}
	return err
}

// Destroy removes a session.
func (s *SessionStore) Destroy(ctx context.Context, token string) error {
	return s.R.Del(ctx, key(token)).Err()
}
