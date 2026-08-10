package api

import "testing"

// TestManagedTaskCronContract guards the cron expressions the create_managed_task
// tool description tells the assistant agent to emit (GC1: AI derives cron from
// natural language). validateCronExpr is the same parser the handler enforces, so
// these must parse, and obviously-wrong inputs must be rejected.
func TestManagedTaskCronContract(t *testing.T) {
	valid := []string{
		"0 9 * * 1", // every Monday 09:00 (周报场景)
		"0 8 * * *", // every day 08:00
		"0 9 1 * *", // 1st of every month 09:00
		"30 17 * * 5",
	}
	for _, expr := range valid {
		if err := validateCronExpr(expr); err != nil {
			t.Errorf("validateCronExpr(%q) = %v, want nil", expr, err)
		}
	}

	invalid := []string{
		"",                 // empty
		"every monday 9am", // natural language must NOT slip through
		"0 9 * *",          // too few fields
		"99 9 * * 1",       // out-of-range minute
	}
	for _, expr := range invalid {
		if err := validateCronExpr(expr); err == nil {
			t.Errorf("validateCronExpr(%q) = nil, want error", expr)
		}
	}
}
