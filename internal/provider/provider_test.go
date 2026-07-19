package provider

import (
	"reflect"
	"testing"
)

func TestProviderRoleOrderAndTransport(t *testing.T) {
	t.Parallel()
	want := []string{"claude", "codex", "glm", "grok", "kimi"}
	if got := ExecutorIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("executor IDs = %v; want %v", got, want)
	}
	if got := SupervisorIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("supervisor IDs = %v; want %v", got, want)
	}
	if transport, ok := TransportFor("glm"); !ok || transport != TransportClaude {
		t.Fatalf("GLM transport = %q, %t", transport, ok)
	}
	if transport, ok := TransportFor("grok"); !ok || transport != TransportGrok {
		t.Fatalf("Grok transport = %q, %t", transport, ok)
	}
	if transport, ok := TransportFor("kimi"); !ok || transport != TransportClaude {
		t.Fatalf("Kimi transport = %q, %t", transport, ok)
	}
	if _, ok := TransportFor("unknown"); ok {
		t.Fatal("unknown provider must not have a transport")
	}
}

func TestEffortsForTransportAndID(t *testing.T) {
	t.Parallel()
	wantClaude := []string{"low", "medium", "high", "xhigh", "max"}
	wantCodex := []string{"minimal", "low", "medium", "high", "xhigh", "max"}
	wantGrok := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	if got := EffortsForTransport(TransportClaude); !reflect.DeepEqual(got, wantClaude) {
		t.Fatalf("claude efforts = %v; want %v", got, wantClaude)
	}
	if got := EffortsForTransport(TransportCodex); !reflect.DeepEqual(got, wantCodex) {
		t.Fatalf("codex efforts = %v; want %v", got, wantCodex)
	}
	if got := EffortsForTransport(TransportGrok); !reflect.DeepEqual(got, wantGrok) {
		t.Fatalf("grok efforts = %v; want %v", got, wantGrok)
	}
	if got := EffortsForTransport(Transport("unknown")); got != nil {
		t.Fatalf("unknown transport efforts = %v; want nil", got)
	}
	// glm rides the Claude transport, so it exposes the Claude effort set.
	if got, ok := EffortsForID("glm"); !ok || !reflect.DeepEqual(got, wantClaude) {
		t.Fatalf("glm efforts = %v, ok=%t; want %v", got, ok, wantClaude)
	}
	if got, ok := EffortsForID("kimi"); !ok || !reflect.DeepEqual(got, wantClaude) {
		t.Fatalf("kimi efforts = %v, ok=%t; want %v", got, ok, wantClaude)
	}
	if got, ok := EffortsForID("codex"); !ok || !reflect.DeepEqual(got, wantCodex) {
		t.Fatalf("codex efforts = %v, ok=%t; want %v", got, ok, wantCodex)
	}
	if _, ok := EffortsForID("unknown"); ok {
		t.Fatal("unknown provider must not report an effort set")
	}
}

func TestSupervisorEffortsUnionCoversEveryProviderValue(t *testing.T) {
	t.Parallel()
	union := SupervisorEfforts()
	seen := map[string]bool{}
	for _, e := range union {
		if seen[e] {
			t.Fatalf("SupervisorEfforts has duplicate %q: %v", e, union)
		}
		seen[e] = true
	}
	// Every supervisor provider's effort value must appear in the union so
	// profile validation without a fixed default_cli never rejects a value the
	// effective supervisor would accept.
	for _, descriptor := range descriptors {
		if !descriptor.Supervisor {
			continue
		}
		for _, e := range EffortsForTransport(descriptor.Transport) {
			if !seen[e] {
				t.Fatalf("union %v missing %q from %s", union, e, descriptor.ID)
			}
		}
	}
}

func TestExecutorEffortsUnionCoversEveryProviderValue(t *testing.T) {
	t.Parallel()
	union := ExecutorEfforts()
	seen := map[string]bool{}
	for _, e := range union {
		if seen[e] {
			t.Fatalf("ExecutorEfforts has duplicate %q: %v", e, union)
		}
		seen[e] = true
	}
	for _, descriptor := range descriptors {
		if !descriptor.Executor {
			continue
		}
		for _, e := range EffortsForTransport(descriptor.Transport) {
			if !seen[e] {
				t.Fatalf("union %v missing %q from %s", union, e, descriptor.ID)
			}
		}
	}
}

func TestAllReturnsDefensiveCopy(t *testing.T) {
	all := All()
	all[0].ID = "changed"
	if got := All()[0].ID; got != "claude" {
		t.Fatalf("provider contract mutated to %q", got)
	}
}
