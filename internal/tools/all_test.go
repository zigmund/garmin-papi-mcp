package tools

import (
	"slices"
	"testing"
)

func TestAllToolsUnique17(t *testing.T) {
	want := []string{
		"get_profile", "get_daily_summary", "get_sleep", "get_heart_rate", "get_hrv",
		"get_body_battery", "get_stress", "get_steps", "get_spo2", "list_activities",
		"get_activity", "get_weight", "get_nutrition", "get_hydration", "get_fitness_age",
		"get_wellness_activities", "get_menstrual_cycle",
	}
	all := All()
	if len(all) != 17 {
		t.Fatalf("All() has %d tools, want 17", len(all))
	}
	seen := map[string]bool{}
	for _, tool := range all {
		if seen[tool.Name] {
			t.Errorf("duplicate tool %q", tool.Name)
		}
		seen[tool.Name] = true
		if tool.Handler == nil {
			t.Errorf("tool %q has no handler", tool.Name)
		}
	}
	got := make([]string, 0, len(seen))
	for n := range seen {
		got = append(got, n)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("tool names = %v, want %v", got, want)
	}
}

// Callers get their own slice: changing it does not change later results.
func TestAllReturnsFreshSlice(t *testing.T) {
	a := All()
	a[0].Name = "mutated"
	a = append(a[:1], a[2:]...)
	_ = a
	b := All()
	if len(b) != 17 || b[0].Name == "mutated" {
		t.Fatalf("All() was affected by a caller's mutation: %d tools, first %q", len(b), b[0].Name)
	}
}
