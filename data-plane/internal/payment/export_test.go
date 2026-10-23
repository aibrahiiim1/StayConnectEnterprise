package payment

import "github.com/jackc/pgx/v5/pgxpool"

// Test-only seams, compiled only for this package's tests, so production cannot build an engine with a chosen config or
// provider.

// NewEngine defaults the outcome credential to the same pool; authority-split tests use NewEngineWithOutcome.
func NewEngine(cfg Config, pool *pgxpool.Pool, p Provider, g Granter) *Engine {
	e := newEngine(cfg, pool, p, g)
	e.outcomePool = pool
	return e
}

// NewEngineWithOutcome takes a separate outcome connection; nil models the DARK state with no outcome credential.
func NewEngineWithOutcome(cfg Config, pool, outcome *pgxpool.Pool, p Provider, g Granter) *Engine {
	e := newEngine(cfg, pool, p, g)
	e.outcomePool = outcome
	return e
}

func ProductionProviderFor(cfg Config) (Provider, error) { return productionProvider(cfg) }
