package provider_test

import (
	"reflect"
	"testing"

	"github.com/shinpr/galley/internal/daemonconfig"
	"github.com/shinpr/galley/internal/provider"
	"github.com/shinpr/galley/internal/task"
)

func TestProviderMembershipConsumersStayAligned(t *testing.T) {
	t.Parallel()
	if got, want := task.ExecutorCLIEnum(), provider.ExecutorIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("task executor enum = %v; provider contract = %v", got, want)
	}
	if got, want := daemonconfig.SupervisorCLIs(), provider.SupervisorIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("daemon supervisor enum = %v; provider contract = %v", got, want)
	}
}
