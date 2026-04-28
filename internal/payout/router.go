package payout

import "fmt"

// Router resolves a rail name to its implementation. The orchestrator
// builds one router at boot, registers every rail it knows how to
// drive, and hands it to the saga step. The step looks up by the
// rail name persisted on payouts.rail, which keeps the schema
// decoupled from Go type identity and lets a deploy add or rename
// rails without a DB migration.
type Router struct {
	rails map[string]Rail
}

// NewRouter wires every rail at construction time. Empty router is
// allowed (useful in tests that only assert wiring); a saga step
// that hits an unknown rail surfaces ErrUnknownRail.
func NewRouter(rails ...Rail) *Router {
	r := &Router{rails: make(map[string]Rail, len(rails))}
	for _, rail := range rails {
		r.rails[rail.Name()] = rail
	}
	return r
}

// Get returns the rail registered under name, or ErrUnknownRail.
func (r *Router) Get(name string) (Rail, error) {
	rail, ok := r.rails[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownRail, name)
	}
	return rail, nil
}

// Names returns the list of registered rail names. Useful for boot
// log lines and operator visibility.
func (r *Router) Names() []string {
	out := make([]string, 0, len(r.rails))
	for name := range r.rails {
		out = append(out, name)
	}
	return out
}
