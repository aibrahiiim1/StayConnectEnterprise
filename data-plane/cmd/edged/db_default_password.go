package main

// An appliance initialised before provisioning generated a password keeps the published default. edged tries
// that login (no query, closed at once) and the Overview warns until it is refused; it warns only and never
// changes the password. The answer is cached for six hours.

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	defaultDBUser          = "stayconnect"
	defaultDBPassword      = "stayconnect"
	defaultDBPasswordEvery = 6 * time.Hour
)

type defaultDBPasswordCheck struct {
	mu      sync.Mutex
	checked time.Time
	known   bool
	found   bool
	// probe: ok is false when the answer is unknown (unreachable, or refused for another reason), which raises nothing.
	probe func(ctx context.Context) (found, ok bool)
}

func newDefaultDBPasswordCheck(host string, port uint16) *defaultDBPasswordCheck {
	return &defaultDBPasswordCheck{probe: func(ctx context.Context) (bool, bool) {
		cfg, err := pgx.ParseConfig("sslmode=disable dbname=postgres")
		if err != nil {
			return false, false
		}
		cfg.Host, cfg.Port, cfg.User, cfg.Password = host, port, defaultDBUser, defaultDBPassword
		cfg.ConnectTimeout = 3 * time.Second
		conn, err := pgx.ConnectConfig(ctx, cfg)
		if err == nil {
			_ = conn.Close(ctx)
			return true, true
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && (pgErr.Code == "28P01" || pgErr.Code == "28000") {
			return false, true
		}
		return false, false
	}}
}

func (c *defaultDBPasswordCheck) inUse(ctx context.Context) bool {
	if c == nil || c.probe == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.checked.IsZero() || time.Since(c.checked) >= defaultDBPasswordEvery {
		pctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		found, ok := c.probe(pctx)
		cancel()
		if ok || !c.known {
			c.found, c.known = found, ok
		}
		c.checked = time.Now()
	}
	return c.known && c.found
}

func defaultDBPasswordAttention() attentionItem {
	return attentionItem{ID: "db-default-password", Severity: "warn",
		Title: "The database still has its default password",
		Detail: "It holds guest data. Change it as described in the security hardening guide " +
			"(Database superuser password); this warning clears once the default is refused.",
		Href: "/health", Action: "Open diagnostics"}
}
