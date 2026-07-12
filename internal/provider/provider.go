// Package provider owns supported provider identities and capabilities.
package provider

import "slices"

type Transport string

const (
	TransportClaude Transport = "claude"
	TransportCodex  Transport = "codex"
)

type Descriptor struct {
	ID         string
	Transport  Transport
	Executor   bool
	Supervisor bool
}

var descriptors = []Descriptor{
	{ID: "claude", Transport: TransportClaude, Executor: true, Supervisor: true},
	{ID: "codex", Transport: TransportCodex, Executor: true, Supervisor: true},
	{ID: "glm", Transport: TransportClaude, Executor: true, Supervisor: true},
}

// Galley validates provider-level values and leaves model compatibility to each CLI.
var (
	claudeEfforts = []string{"low", "medium", "high", "xhigh", "max"}
	codexEfforts  = []string{"minimal", "low", "medium", "high", "xhigh", "max"}
)

func All() []Descriptor { return slices.Clone(descriptors) }

func Lookup(id string) (Descriptor, bool) {
	for _, descriptor := range descriptors {
		if descriptor.ID == id {
			return descriptor, true
		}
	}
	return Descriptor{}, false
}

func ExecutorIDs() []string { return roleIDs(func(d Descriptor) bool { return d.Executor }) }

func SupervisorIDs() []string { return roleIDs(func(d Descriptor) bool { return d.Supervisor }) }

func IsExecutor(id string) bool {
	descriptor, ok := Lookup(id)
	return ok && descriptor.Executor
}

func IsSupervisor(id string) bool {
	descriptor, ok := Lookup(id)
	return ok && descriptor.Supervisor
}

func TransportFor(id string) (Transport, bool) {
	descriptor, ok := Lookup(id)
	return descriptor.Transport, ok
}

// EffortsForTransport returns the accepted reasoning-effort values for a
// transport in stable order. An unknown transport returns nil.
func EffortsForTransport(t Transport) []string {
	switch t {
	case TransportClaude:
		return slices.Clone(claudeEfforts)
	case TransportCodex:
		return slices.Clone(codexEfforts)
	default:
		return nil
	}
}

// EffortsForID returns the accepted reasoning-effort values for a provider id.
// The bool is false when the id is unknown.
func EffortsForID(id string) ([]string, bool) {
	descriptor, ok := Lookup(id)
	if !ok {
		return nil, false
	}
	return EffortsForTransport(descriptor.Transport), true
}

// SupervisorEfforts returns the stable union used before an effective provider is known.
func SupervisorEfforts() []string {
	var out []string
	seen := map[string]bool{}
	for _, descriptor := range descriptors {
		if !descriptor.Supervisor {
			continue
		}
		for _, effort := range EffortsForTransport(descriptor.Transport) {
			if !seen[effort] {
				seen[effort] = true
				out = append(out, effort)
			}
		}
	}
	return out
}

func roleIDs(include func(Descriptor) bool) []string {
	ids := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if include(descriptor) {
			ids = append(ids, descriptor.ID)
		}
	}
	return ids
}
