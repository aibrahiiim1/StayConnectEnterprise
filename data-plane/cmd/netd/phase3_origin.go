package main

// netd, as the tc owner, registers the counters it read right after creating a class as the accounting origin,
// so acctd's first pass has a real starting point instead of silently baselining and discarding the first
// seconds of traffic. It registers what it read and never assumes zero (an adopted class would not be zero).

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

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

// A failure is reported, never swallowed: without an origin the traffic before the first observation is lost
// silently. The caller holds p.mu.
func (p *phase3Shaping) registerOrigin(ctx context.Context, s shapeplan.Session, minor int, epoch int64) string {
	if p.origins == nil {
		return "" // no registrar wired (unit tests drive apply() directly); nothing to report
	}
	if s.DeviceID == "" {
		return "class origin not registered for " + s.SessionID + ": the plan carries no device identity"
	}
	up, upErr := p.shp.ReadClasses(ctx, shape.IFBName(s.Bridge))
	down, downErr := p.shp.ReadClasses(ctx, s.Bridge)
	if upErr != nil || downErr != nil {
		return "class origin not registered for " + s.SessionID + ": counters unreadable at creation"
	}
	return p.registerOriginWith(ctx, s, minor, epoch, up, down)
}

// registerOriginWith records the origin from counters the caller has just read; a batched admission reads each device once for all its sessions.
func (p *phase3Shaping) registerOriginWith(ctx context.Context, s shapeplan.Session, minor int, epoch int64, up, down map[int]shape.ClassBytes) string {
	if p.origins == nil {
		return ""
	}
	if s.DeviceID == "" {
		return "class origin not registered for " + s.SessionID + ": the plan carries no device identity"
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
