package posting

// Test seams: compiled only for this package's tests, so NewProductionEngine is the only production construction path.

// NewEngine still routes through newEngine, so the DARK guard applies as in production.
func NewEngine(cfg Config, repo *Repo, inner Transport) *Engine { return newEngine(cfg, repo, inner) }

func ProductionTransportFor(cfg Config) (Transport, error) { return productionTransport(cfg) }

func NewDarkGuard(cfg Config, inner Transport) *DarkGuard { return newDarkGuard(cfg, inner) }

// ProductionEngineWithEnv drives the loader with a supplied environment; deliberately no transport argument.
func ProductionEngineWithEnv(repo *Repo, getenv Getenv) (*Engine, error) {
	cfg, err := LoadConfigFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	inner, err := productionTransport(cfg)
	if err != nil {
		return nil, err
	}
	return newEngine(cfg, repo, inner), nil
}
