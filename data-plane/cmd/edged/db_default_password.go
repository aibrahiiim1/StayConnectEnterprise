package main

// THE DATABASE STILL ACCEPTS ITS DEFAULT SUPERUSER PASSWORD (GDPR audit 2026-10-19, C5; Product-Owner decision
// 2026-10-19: warn, do not change it automatically; docs/SECURITY_HARDENING.md, audit N-10).
//
// An appliance whose database was initialised before provisioning generated a password keeps the published
// default. The database listens on the appliance only, so the risk is limited, but it holds guest data
// (GDPR Art. 32). edged tries that one login itself -- no query, the connection is closed at once -- and the
// Overview warns until it is refused. The answer is kept for six hours, so the database sees one attempt then.

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
	// probe reports whether the default login succeeds; ok is false when the answer is not known (the database
	// is unreachable, or refused for another reason), which raises nothing.
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

// inUse reports whether the default password is known to be accepted. The probe runs at most every six hours.
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
