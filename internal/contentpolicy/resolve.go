package contentpolicy

import "sync"

// Resolver freezes explicit policy inputs at construction and memoizes both
// success and failure. Process environment projection is owned by the CLI
// wiring layer, not by this deterministic core.
type Resolver struct {
	configDir   string
	environment Environment
	once        sync.Once
	resolved    *Resolved
	err         error
}

func NewResolver(configDir string, environment Environment) *Resolver {
	frozen := environment
	if environment.ExpectedOwnerUID != nil {
		uid := *environment.ExpectedOwnerUID
		frozen.ExpectedOwnerUID = &uid
	}
	return &Resolver{configDir: configDir, environment: frozen}
}

func (resolver *Resolver) Resolve() (*Resolved, error) {
	resolver.once.Do(func() {
		resolver.resolved, resolver.err = Load(resolver.configDir, resolver.environment)
	})
	return cloneResolved(resolver.resolved), resolver.err
}
