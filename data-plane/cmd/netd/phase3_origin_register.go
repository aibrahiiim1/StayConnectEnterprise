package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

// A failure is reported, never swallowed: without an origin the traffic before the first observation is lost
// silently. The caller holds p.mu. ended is true when durable state says the session has already ended: its access is
// then withdrawn rather than reported as a failed registration.
func (p *phase3Shaping) registerOrigin(ctx context.Context, s shapeplan.Session, minor int, epoch int64) (problem string, ended bool) {
	if p.origins == nil {
		return "", false // no registrar wired (unit tests drive apply() directly); nothing to report
	}
	if s.DeviceID == "" {
		return "class origin not registered for " + s.SessionID + ": the plan carries no device identity", false
	}
	up, upErr := p.shp.ReadClasses(ctx, shape.IFBName(s.Bridge))
	down, downErr := p.shp.ReadClasses(ctx, s.Bridge)
	if upErr != nil || downErr != nil {
		return "class origin not registered for " + s.SessionID + ": counters unreadable at creation", false
	}
	return p.registerOriginWith(ctx, s, minor, epoch, up, down)
}

// registerOriginWith records the origin from counters the caller has just read; a batched admission reads each device once for all its sessions.
func (p *phase3Shaping) registerOriginWith(ctx context.Context, s shapeplan.Session, minor int, epoch int64, up, down map[int]shape.ClassBytes) (string, bool) {
	if p.origins == nil {
		return "", false
	}
	if s.DeviceID == "" {
		return "class origin not registered for " + s.SessionID + ": the plan carries no device identity", false
	}
	outcome, err := p.callOrigin(ctx, s, minor, epoch, up, down)
	return p.originVerdict(s, epoch, outcome, err)
}

// callOrigin is the database call alone; it touches no netd state, so a batched admission runs it for many sessions at once.
func (p *phase3Shaping) callOrigin(ctx context.Context, s shapeplan.Session, minor int, epoch int64, up, down map[int]shape.ClassBytes) (string, error) {
	return p.origins.RegisterClassOrigin(ctx, classOrigin{
		Tenant: p.mode.TenantID, Site: p.mode.SiteID,
		SessionID: s.SessionID, DeviceID: s.DeviceID, Bridge: s.Bridge, ClassMinor: minor, Epoch: epoch,
		OriginUp: int64(up[minor].Bytes), OriginDown: int64(down[minor].Bytes), CreatedAt: time.Now().UTC(),
	})
}

func (p *phase3Shaping) originVerdict(s shapeplan.Session, epoch int64, outcome string, err error) (string, bool) {
	if err != nil {
		if isSessionEndedErr(err) {
			return "", true
		}
		return "class origin not registered for " + s.SessionID + ": " + err.Error(), false
	}
	slog.Info("phase3: accounting origin registered", "session", s.SessionID, "bridge", s.Bridge,
		"epoch", epoch, "outcome", outcome)
	return "", false
}
