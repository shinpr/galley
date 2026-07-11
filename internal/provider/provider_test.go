package provider

import (
	"reflect"
	"testing"
)

func TestProviderRoleOrderAndTransport(t *testing.T) {
	t.Parallel()
	want := []string{"claude", "codex", "glm"}
	if got := ExecutorIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("executor IDs = %v; want %v", got, want)
	}
	if got := SupervisorIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("supervisor IDs = %v; want %v", got, want)
	}
	if transport, ok := TransportFor("glm"); !ok || transport != TransportClaude {
		t.Fatalf("GLM transport = %q, %t", transport, ok)
	}
	if _, ok := TransportFor("unknown"); ok {
		t.Fatal("unknown provider must not have a transport")
	}
}

func TestAllReturnsDefensiveCopy(t *testing.T) {
	all := All()
	all[0].ID = "changed"
	if got := All()[0].ID; got != "claude" {
		t.Fatalf("provider contract mutated to %q", got)
	}
}
