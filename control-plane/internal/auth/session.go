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
	IsSuperAdmin    bool      `json:"is_super_admin"`
	DefaultTenantID string    `json:"default_tenant_id,omitempty"` // "" if super admin
	Roles           []string  `json:"roles"`
	SiteIDs         []string  `json:"site_ids,omitempty"`    // explicit site bindings
	TenantWide      bool      `json:"tenant_wide,omitempty"` // may act across tenant sites
	CreatedAt       time.Time `json:"created_at"`
	LastSeenAt      time.Time `json:"last_seen_at"`
	ExpiresAt       time.Time `json:"expires_at"`
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

// Get fetches a session by token. Returns (nil, nil) when not found.
// Also slides the TTL forward if the session is more than 1 min old.
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
	if time.Since(sess.LastSeenAt) > time.Minute {
		sess.LastSeenAt = time.Now()
		sess.ExpiresAt = sess.LastSeenAt.Add(SessionTTL)
		if buf, err := json.Marshal(sess); err == nil {
			_ = s.R.Set(ctx, key(token), buf, SessionTTL).Err()
		}
	}
	return &sess, nil
}

// Destroy removes a session.
func (s *SessionStore) Destroy(ctx context.Context, token string) error {
	return s.R.Del(ctx, key(token)).Err()
}
