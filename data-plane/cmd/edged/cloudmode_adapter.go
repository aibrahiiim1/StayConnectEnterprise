package main

// edgedQuerier adapts edged's pool to cloudmode.Querier, for the same reason scd has one: the package that
// answers "may this appliance talk to Central" must not depend on a database driver.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/cloudmode"
)

type edgedQuerier struct{ p *pgxpool.Pool }

func (q edgedQuerier) QueryRow(ctx context.Context, sql string, args ...any) cloudmode.Row {
	return q.p.QueryRow(ctx, sql, args...)
}
