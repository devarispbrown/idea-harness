package cli

import (
	"encoding/json"
	"net/http"
	"testing"
)

// An assertion's code (ASM-n, what gaps and the PRD cite) resolves exactly and
// case-insensitively, ahead of id-prefix matching.
func TestAssumptionCommandResolvesCode(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/assertions", []map[string]any{
		{"id": "abcd1234ffff", "code": "ASM-1", "type": "assumption", "statement": "users have one meter"},
		{"id": "abcd9999ffff", "code": "ASM-12", "type": "hypothesis", "statement": "usage billing lifts revenue"},
	})
	var body map[string]string
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/assertions/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": r.PathValue("id"), "status": body["status"]})
	})
	if _, _, err := run(t, "assumption", "asm-12", "retract"); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "retracted" || !f.asked("/assertions/abcd9999ffff/status") {
		t.Errorf("body=%v requests=%v", body, f.requests)
	}
}
