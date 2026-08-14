package payment

import "github.com/jackc/pgx/v5/pgxpool"

// Test-only seams. This file is compiled ONLY for this package's own tests, so a production binary cannot
// construct an engine with a chosen config or a chosen provider -- NewProductionEngine remains the whole
// production surface.

// NewEngine builds an engine with an explicit posture and provider double. The outcome credential
// defaults to the same pool so existing tests keep exercising the LOGIC; the tests that are about the
// AUTHORITY split use NewEngineWithOutcome below and pass a genuinely different connection.
func NewEngine(cfg Config, pool *pgxpool.Pool, p Provider, g Granter) *Engine {
	e := newEngine(cfg, pool, p, g)
	e.outcomePool = pool
	return e
}

// NewEngineWithOutcome builds an engine whose outcome authority is a SEPARATE connection. Passing nil
// models the delivered DARK state, in which no outcome credential is configured at all.
func NewEngineWithOutcome(cfg Config, pool, outcome *pgxpool.Pool, p Provider, g Granter) *Engine {
	e := newEngine(cfg, pool, p, g)
	e.outcomePool = outcome
	return e
}

// ProductionProviderFor exposes the production factory so a test can assert what a real build is handed.
func ProductionProviderFor(cfg Config) (Provider, error) { return productionProvider(cfg) }
