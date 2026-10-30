package main

// netd, as the tc owner, registers the counters it read right after creating a class as the accounting origin,
// so acctd's first pass has a real starting point instead of silently baselining and discarding the first
// seconds of traffic. It registers what it read and never assumes zero (an adopted class would not be zero).

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
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
