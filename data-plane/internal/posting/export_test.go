package posting

// Deterministic test seams. This file is named *_test.go, so the Go toolchain compiles it ONLY when
// building this package's own tests. Production binaries do not contain these symbols and cannot call
// them, which is what makes NewProductionEngine the single production construction path in fact rather
// than by convention.

// NewEngine builds an engine with an explicit config and transport, for tests that need to drive a
// specific flag posture or a deterministic stub. It still routes through newEngine, so the DARK guard is
// applied here exactly as it is in production.
func NewEngine(cfg Config, repo *Repo, inner Transport) *Engine { return newEngine(cfg, repo, inner) }

// ProductionTransportFor exposes the production transport factory so a test can assert what a real build
// would actually be handed.
func ProductionTransportFor(cfg Config) (Transport, error) { return productionTransport(cfg) }

// NewDarkGuard builds a guard directly, for transport-level tests.
func NewDarkGuard(cfg Config, inner Transport) *DarkGuard { return newDarkGuard(cfg, inner) }

// ProductionEngineWithEnv drives the production constructor's LOADER with a supplied environment, so a
// test can assert the posture a given deployment would produce. It deliberately does NOT accept a
// transport: the transport still comes from the production factory, exactly as it does in a real build.
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
