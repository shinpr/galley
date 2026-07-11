// Package provider defines Galley's supported model provider identities and
// mechanical capabilities without depending on runtime or configuration code.
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

func roleIDs(include func(Descriptor) bool) []string {
	ids := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if include(descriptor) {
			ids = append(ids, descriptor.ID)
		}
	}
	return ids
}
