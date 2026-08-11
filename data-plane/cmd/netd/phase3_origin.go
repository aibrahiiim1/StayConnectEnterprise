package main

// THE ACCOUNTING ORIGIN, registered by the process that creates the class.
//
// There is a window between "netd installs a managed class" and "acctd's next periodic pass reads it". On a
// tick interval that window is seconds of real traffic, for every session, forever — and it is invisible,
// because the first observation legitimately has nothing to subtract from and BASELINES. A baseline is a
// normal outcome, so nothing anywhere looks wrong while every guest's first seconds are discarded.
//
// Only the TC owner can close it. It is the only process that knows the instant a class came into existence,
// and it is the only one that can read the counters BEFORE the guest can push a packet through it. So netd
// registers the origin — the counters it actually read immediately after creation — through the controlled
// operation, and acctd's first pass then measures a difference from a real starting point instead of
// inventing one.
//
// Note what this deliberately does NOT do: it never asserts zero. A class netd created is expected to read
// zero, but "expected" is not evidence, and a class adopted rather than created would not. Whatever the owner
// read is what gets registered.

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

// originRegistrar records a newly created managed class's accounting origin.
type originRegistrar interface {
	RegisterClassOrigin(ctx context.Context, o classOrigin) (string, error)
}

type classOrigin struct {
	Tenant, Site string
	SessionID    string
	DeviceID     string
	Bridge       string
	ClassMinor   int
	Epoch        int64
	OriginUp     int64
	OriginDown   int64
	CreatedAt    time.Time
}

// pgOrigins is the database-backed registrar.
type pgOrigins struct {
	pool interface {
		QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	}
}

func (p *pgOrigins) RegisterClassOrigin(ctx context.Context, o classOrigin) (string, error) {
	var outcome string
	err := p.pool.QueryRow(ctx, `SELECT iam_v2.register_class_origin($1,$2,$3::uuid,$4::uuid,$5,$6,$7,$8,$9,$10)`,
		o.Tenant, o.Site, o.SessionID, o.DeviceID, o.Bridge, o.ClassMinor, o.Epoch,
		o.OriginUp, o.OriginDown, o.CreatedAt).Scan(&outcome)
	return outcome, err
}

// registerOrigin reads the class's counters immediately after creation and records them as the accounting
// origin. A failure here is reported, never swallowed: without an origin the first ordinary observation
// silently baselines, and the traffic in between is lost with nothing to show it ever existed.
//
// The caller holds p.mu, so this runs before any further plan can be applied to the same class.
func (p *phase3Shaping) registerOrigin(ctx context.Context, s shapeplan.Session, minor int, epoch int64) string {
	if p.origins == nil {
		return "" // no registrar wired (unit tests drive apply() directly); nothing to report
	}
	if s.DeviceID == "" {
		return "class origin not registered for " + s.SessionID + ": the plan carries no device identity"
	}
	// Read what is actually there. The expected value is zero for a class created a moment ago, but the
	// point of an origin is to record evidence rather than an expectation.
	up, upErr := p.shp.ReadClasses(ctx, shape.IFBName(s.Bridge))
	down, downErr := p.shp.ReadClasses(ctx, s.Bridge)
	if upErr != nil || downErr != nil {
		return "class origin not registered for " + s.SessionID + ": counters unreadable at creation"
	}
	outcome, err := p.origins.RegisterClassOrigin(ctx, classOrigin{
		Tenant: p.mode.TenantID, Site: p.mode.SiteID,
		SessionID: s.SessionID, DeviceID: s.DeviceID, Bridge: s.Bridge, ClassMinor: minor, Epoch: epoch,
		OriginUp: int64(up[minor].Bytes), OriginDown: int64(down[minor].Bytes), CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return "class origin not registered for " + s.SessionID + ": " + err.Error()
	}
	slog.Info("phase3: accounting origin registered", "session", s.SessionID, "bridge", s.Bridge,
		"epoch", epoch, "outcome", outcome)
	return ""
}
