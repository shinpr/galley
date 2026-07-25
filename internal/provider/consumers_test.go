package provider_test

import (
	"reflect"
	"testing"

	"github.com/shinpr/galley/internal/provider"
	"github.com/shinpr/galley/internal/task"
)

func TestProviderMembershipConsumersStayAligned(t *testing.T) {
	t.Parallel()
	if got, want := task.ExecutorCLIEnum(), provider.ExecutorIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("task executor enum = %v; provider contract = %v", got, want)
	}
}
