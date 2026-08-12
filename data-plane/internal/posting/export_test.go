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
