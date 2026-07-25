package task

import (
	"encoding/json"
	"testing"
)

func TestLoopBudgetJSONMatchesIntegerTaskContract(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   LoopBudget
		want string
	}{
		{name: "default", in: LoopBudget{}, want: "10"},
		{name: "limited", in: LoopBudget{Count: 3, Set: true}, want: "3"},
		{name: "unlimited", in: LoopBudget{Count: 0, Set: true}, want: "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("JSON got %s, want %s", got, tc.want)
			}
		})
	}
}
