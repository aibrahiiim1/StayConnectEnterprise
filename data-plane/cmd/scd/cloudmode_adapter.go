package main

// pgxQuerier adapts a pgx pool to cloudmode.Querier.
//
// The adapter exists so internal/cloudmode depends on no database driver: that package answers one question
// in one place for scd, edged and the operator screen, and a test for it should not need a Postgres.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/cloudmode"
)

type pgxQuerier struct{ p *pgxpool.Pool }

func (q pgxQuerier) QueryRow(ctx context.Context, sql string, args ...any) cloudmode.Row {
	return q.p.QueryRow(ctx, sql, args...)
}
