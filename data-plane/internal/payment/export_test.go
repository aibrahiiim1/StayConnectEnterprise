package payment

import "github.com/jackc/pgx/v5/pgxpool"

// Test-only seams. This file is compiled ONLY for this package's own tests, so a production binary cannot
// construct an engine with a chosen config or a chosen provider -- NewProductionEngine remains the whole
// production surface.

// NewEngine builds an engine with an explicit posture and provider double.
func NewEngine(cfg Config, pool *pgxpool.Pool, p Provider, g Granter) *Engine {
	return newEngine(cfg, pool, p, g)
}

// ProductionProviderFor exposes the production factory so a test can assert what a real build is handed.
func ProductionProviderFor(cfg Config) (Provider, error) { return productionProvider(cfg) }
