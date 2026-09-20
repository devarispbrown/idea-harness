package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// This file covers the judgment-loop CLI commands added because `internal/cli`
// had no caller for POST …/questions/{id}/answer or …/decisions/{id}/resolve:
// an evaluator answering a real question had to reach past the CLI into raw
// HTTP. See judgment.go's "the judgment loop" section.

func TestQuestionsListsAndShowsShortRefs(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/questions", []map[string]any{
		{"id": "41d2b350b21baa8b875585217bf0bf94", "question": "Where do API keys live?", "status": "open"},
		{"id": "3e594d1a5a385f94f59052a40c006220", "question": "What is the rate limit?", "status": "resolved"},
	})

	stdout, _, err := run(t, "questions")
	if err != nil {
		t.Fatalf("idea questions: %v", err)
	}
	if !strings.Contains(stdout, "41d2b350") {
		t.Errorf("expected a short ref in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Where do API keys live?") {
		t.Errorf("expected the question text:\n%s", stdout)
	}
	if !strings.Contains(stdout, "(1 open)") {
		t.Errorf("expected an open count:\n%s", stdout)
	}
}

func TestQuestionsJSON(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/questions", []map[string]any{
		{"id": "q1", "question": "Q?", "status": "open"},
	})
	stdout, _, err := run(t, "questions", "--json")
	if err != nil {
		t.Fatalf("idea questions --json: %v", err)
	}
	var out []cliQuestion
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, stdout)
	}
	if len(out) != 1 || out[0].ID != "q1" {
		t.Errorf("unexpected output: %+v", out)
	}
}

// TestAnswerResolvesByFullIDOrPrefix covers the reference form `idea
// questions` prints and `idea answer` must accept back: the full id, and an
// unambiguous prefix of it (questions have no human code, unlike decisions).
func TestAnswerResolvesByFullIDOrPrefix(t *testing.T) {
	for _, ref := range []string{"41d2b350b21baa8b875585217bf0bf94", "41d2b350"} {
		t.Run(ref, func(t *testing.T) {
			f := newFakeDaemon(t)
			f.seedInitiative()
			f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/questions", []map[string]any{
				{"id": "41d2b350b21baa8b875585217bf0bf94", "question": "Where do API keys live?", "status": "open"},
			})
			var body string
			f.mux.HandleFunc("POST /api/v1/projects/{projectId}/questions/{id}/answer",
				func(w http.ResponseWriter, r *http.Request) {
					raw, _ := io.ReadAll(r.Body)
					body = string(raw)
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{"status": "resolved"})
				})

			stdout, _, err := run(t, "answer", ref, "An environment variable, never a file.")
			if err != nil {
				t.Fatalf("idea answer %s: %v", ref, err)
			}
			if !strings.Contains(stdout, "answered") {
				t.Errorf("no confirmation printed:\n%s", stdout)
			}
			if !strings.Contains(body, "An environment variable") {
				t.Errorf("answer text not sent, got body %q", body)
			}
			if !f.asked("/questions/41d2b350b21baa8b875585217bf0bf94/answer") {
				t.Errorf("did not resolve to the full id; requests: %v", f.requests)
			}
		})
	}
}

func TestAnswerRejectsAnAmbiguousPrefix(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/questions", []map[string]any{
		{"id": "aaaa1111", "question": "Q1?", "status": "open"},
		{"id": "aaaa2222", "question": "Q2?", "status": "open"},
	})
	_, _, err := run(t, "answer", "aaaa", "some answer")
	if err == nil {
		t.Fatal("idea answer aaaa: want an error for an ambiguous prefix, got nil")
	}
	if !strings.Contains(err.Error(), "more than one question") {
		t.Errorf("error should explain the ambiguity, got: %v", err)
	}
}

func TestAnswerRejectsAnUnknownQuestion(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/questions", []map[string]any{})
	_, _, err := run(t, "answer", "nope", "some answer")
	if err == nil {
		t.Fatal("want an error for an unknown question, got nil")
	}
	if !strings.Contains(err.Error(), "idea questions") {
		t.Errorf("error should point at `idea questions`, got: %v", err)
	}
}

// TestResolveByOptionNumber covers the option-index form (a decision usually
// has options; the model's own recommendation prose does not always match one
// verbatim, so the CLI accepts a plain 1-based index too).
func TestResolveByOptionNumber(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/decisions", []map[string]any{
		{"id": "dec-1", "code": "DEC-1", "question": "Which window?", "options": []string{"Fixed window", "Sliding window"}, "status": "open"},
	})
	var body string
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/decisions/{id}/resolve",
		func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "decided"})
		})

	stdout, _, err := run(t, "resolve", "DEC-1", "2")
	if err != nil {
		t.Fatalf("idea resolve DEC-1 2: %v", err)
	}
	if !strings.Contains(stdout, "Sliding window") {
		t.Errorf("resolved output should name the chosen option:\n%s", stdout)
	}
	if !strings.Contains(body, "Sliding window") {
		t.Errorf("request body should carry the option text, got %q", body)
	}
	if !f.asked("/decisions/dec-1/resolve") {
		t.Errorf("did not resolve DEC-1 to its raw id; requests: %v", f.requests)
	}
}

func TestResolveByFreeText(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/decisions", []map[string]any{
		{"id": "dec-2", "code": "DEC-2", "question": "How many replicas?", "options": []string{}, "status": "open"},
	})
	var body string
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/decisions/{id}/resolve",
		func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "decided"})
		})
	stdout, _, err := run(t, "resolve", "DEC-2", "Three, behind a load balancer")
	if err != nil {
		t.Fatalf("idea resolve DEC-2: %v", err)
	}
	if !strings.Contains(stdout, "Three, behind a load balancer") {
		t.Errorf("output should echo the free-text resolution:\n%s", stdout)
	}
	if !strings.Contains(body, "Three, behind a load balancer") {
		t.Errorf("request body should carry the free text, got %q", body)
	}
}

func TestResolveRejectsAnOutOfRangeOption(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/decisions", []map[string]any{
		{"id": "dec-1", "code": "DEC-1", "question": "Which window?", "options": []string{"A", "B"}, "status": "open"},
	})
	_, _, err := run(t, "resolve", "DEC-1", "5")
	if err == nil {
		t.Fatal("want an error for an out-of-range option, got nil")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("error should say the option is out of range, got: %v", err)
	}
}

func TestReopenDecisionCallsTheReopenRoute(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/decisions", []map[string]any{
		{"id": "dec-1", "code": "DEC-1", "question": "Which window?", "status": "decided"},
	})
	f.json("POST /api/v1/projects/{projectId}/decisions/{id}/reopen", map[string]string{"status": "open"})

	stdout, _, err := run(t, "reopen-decision", "DEC-1")
	if err != nil {
		t.Fatalf("idea reopen-decision DEC-1: %v", err)
	}
	if !strings.Contains(stdout, "reopened") {
		t.Errorf("no confirmation printed:\n%s", stdout)
	}
	if !f.asked("/decisions/dec-1/reopen") {
		t.Errorf("did not call the reopen route; requests: %v", f.requests)
	}
}

func TestAcceptRecommendationsReportsResolvedAndSkipped(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("POST /api/v1/projects/{projectId}/initiatives/{id}/decisions/accept-recommendations",
		map[string]any{
			"resolved": []string{"DEC-1"},
			"skipped":  []map[string]string{{"code": "DEC-2", "reason": "no recommendation was given"}},
		})

	stdout, _, err := run(t, "accept-recommendations")
	if err != nil {
		t.Fatalf("idea accept-recommendations: %v", err)
	}
	if !strings.Contains(stdout, "DEC-1") || !strings.Contains(stdout, "resolved") {
		t.Errorf("expected DEC-1 reported resolved:\n%s", stdout)
	}
	if !strings.Contains(stdout, "DEC-2") || !strings.Contains(stdout, "no recommendation was given") {
		t.Errorf("expected DEC-2 reported skipped with its reason:\n%s", stdout)
	}
}

func TestTriageRanksPriorityAndOptional(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/triage", map[string]any{
		"items": []map[string]any{
			{"type": "finding", "id": "f1", "label": "SQL injection", "blocking": true, "severity": "critical", "dependents": 0, "rank": "priority"},
			{"type": "question", "id": "q1", "label": "What topology?", "blocking": false, "dependents": 0, "rank": "optional"},
		},
	})

	stdout, _, err := run(t, "triage")
	if err != nil {
		t.Fatalf("idea triage: %v", err)
	}
	if !strings.Contains(stdout, "Priority") || !strings.Contains(stdout, "SQL injection") {
		t.Errorf("expected the priority group with its item:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Optional") || !strings.Contains(stdout, "What topology?") {
		t.Errorf("expected the optional group with its item:\n%s", stdout)
	}
}

func TestTriageJSON(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/triage", map[string]any{
		"items": []map[string]any{
			{"type": "decision", "id": "d1", "label": "Which window?", "blocking": false, "dependents": 2, "rank": "priority"},
		},
	})
	stdout, _, err := run(t, "triage", "--json")
	if err != nil {
		t.Fatalf("idea triage --json: %v", err)
	}
	var items []cliTriageItem
	if err := json.Unmarshal([]byte(stdout), &items); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, stdout)
	}
	if len(items) != 1 || items[0].ID != "d1" || items[0].Dependents != 2 {
		t.Errorf("unexpected output: %+v", items)
	}
}
