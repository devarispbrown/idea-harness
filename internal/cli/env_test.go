package cli

import (
	"strings"
	"testing"
)

// seedEnvTasks registers an initiative with two tasks, one of which declares
// environment variables (one duplicated across the two tasks to exercise the
// dedupe in envVarsForInitiative).
func seedEnvTasks(f *fakeDaemon) {
	seedTaskInitiative(f)
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/tasks", []map[string]any{
		{
			"id": "task-1", "code": "TASK-1", "title": "Checkout", "priority": "must",
			"env_vars": []map[string]any{
				{"name": "STRIPE_SECRET_KEY", "how": "create a restricted key in the Stripe dashboard", "url": "https://dashboard.stripe.com/apikeys"},
				{"name": "ZOHO_API_KEY", "how": "generate it under Zoho API credentials"},
			},
		},
		{
			"id": "task-2", "code": "TASK-2", "title": "Refunds", "priority": "should",
			"env_vars": []map[string]any{
				{"name": "STRIPE_SECRET_KEY", "how": "duplicate declaration, must not repeat"},
			},
		},
	})
}

func TestEnvListAggregatesAndDeduplicates(t *testing.T) {
	f := newFakeDaemon(t)
	seedEnvTasks(f)

	stdout, _, err := run(t, "env")
	if err != nil {
		t.Fatalf("idea env: %v", err)
	}
	for _, want := range []string{"STRIPE_SECRET_KEY", "ZOHO_API_KEY", "Stripe dashboard", "dashboard.stripe.com", "Zoho API"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("expected %q in `idea env` output:\n%s", want, stdout)
		}
	}
	// The duplicate STRIPE_SECRET_KEY from TASK-2 must not appear twice.
	if strings.Count(stdout, "STRIPE_SECRET_KEY") != 1 {
		t.Errorf("STRIPE_SECRET_KEY should appear exactly once (deduplicated):\n%s", stdout)
	}
}

func TestEnvListJSON(t *testing.T) {
	f := newFakeDaemon(t)
	seedEnvTasks(f)

	stdout, _, err := run(t, "env", "--json")
	if err != nil {
		t.Fatalf("idea env --json: %v", err)
	}
	for _, want := range []string{`"name": "STRIPE_SECRET_KEY"`, `"name": "ZOHO_API_KEY"`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("expected %s in JSON output:\n%s", want, stdout)
		}
	}
}

func TestEnvSetRefusesUnknownName(t *testing.T) {
	f := newFakeDaemon(t)
	seedEnvTasks(f)
	before := len(f.requests)

	_, _, err := run(t, "env", "set", "NOPE", "--stdin")
	if err == nil {
		t.Fatal("env set with an undeclared name: want an error")
	}
	if !strings.Contains(err.Error(), "`idea env` lists them") {
		t.Errorf("error should point at `idea env`, got: %v", err)
	}
	// The refusal is local once the list is fetched: no write to .env.
	if f.asked("harness.yaml") {
		t.Error("env set reached the .env write for an unknown name")
	}
	if len(f.requests) == before {
		t.Error("expected the tasks list to be fetched before the refusal")
	}
}
