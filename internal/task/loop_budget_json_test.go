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

func TestLoopBudgetJSONRoundTrip(t *testing.T) {
	t.Parallel()
	for _, count := range []int{0, 3} {
		data, err := json.Marshal(LoopBudget{Count: count, Set: true})
		if err != nil {
			t.Fatal(err)
		}
		var got LoopBudget
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if !got.Set || got.Count != count {
			t.Fatalf("round trip got %#v, want set count=%d", got, count)
		}
	}
}

func TestLoopBudgetJSONRejectsInvalidValues(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"null", "-1"} {
		var got LoopBudget
		if err := json.Unmarshal([]byte(value), &got); err == nil {
			t.Fatalf("json.Unmarshal(%s) succeeded, want error", value)
		}
	}
}
