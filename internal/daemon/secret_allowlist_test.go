package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// executeToSecretSweepDenial runs a task whose produced content contains a
// real credential value (an AWS access key shape), returning the resulting
// (denied) change set's secret_sweep finding.
func executeToSecretSweepDenial(t *testing.T, rt *Runtime, ts *httptest.Server, token string) (projectID, iniID string, f domain.Finding) {
	t.Helper()
	projectID, iniID = newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: iniID, Title: "Add key AKIAIOSFODNN7EXAMPLE", Statement: "x", Priority: domain.PriorityMust}
	if err := pr.db.CreateTask(t.Context(), task, "", nil); err != nil {
		t.Fatal(err)
	}
	setStatus(t, rt, projectID, iniID, domain.InitiativePlanApproved)
	opID, err := rt.ExecuteInitiative(t.Context(), projectID, iniID)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	waitForOperation(t, ts, token, opID)

	sets, err := pr.db.ListChangeSets(t.Context(), iniID)
	if err != nil || len(sets) == 0 {
		t.Fatalf("execute produced no change set (err=%v)", err)
	}
	findings, err := pr.db.ListFindingsByTarget(t.Context(), "change_set", sets[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, cand := range findings {
		if cand.Category == "secret_sweep" {
			return projectID, iniID, cand
		}
	}
	t.Fatal("execute did not raise a secret_sweep finding")
	return "", "", domain.Finding{}
}

// TestSecretSweepDenialNamesWhereAndKind is review change #2: the denial
// names path:line and the kind of secret, and never the value.
func TestSecretSweepDenialNamesWhereAndKind(t *testing.T) {
	rt, ts, token := newTestServer(t)
	_, _, f := executeToSecretSweepDenial(t, rt, ts, token)

	if !strings.Contains(f.Finding, ":") {
		t.Errorf("expected the finding to name path:line, got %q", f.Finding)
	}
	if !strings.Contains(f.Finding, "AWS access key ID") {
		t.Errorf("expected the finding to name the kind of secret, got %q", f.Finding)
	}
	if strings.Contains(f.Finding, "AKIAIOSFODNN7EXAMPLE") || strings.Contains(f.Evidence, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("the denial must never echo the credential value: %+v", f)
	}
	if f.SecretFingerprint == "" {
		t.Fatal("expected a secret fingerprint")
	}
}

// TestNotASecretDispositionAllowlistsAndUnblocks is the review changes #1
// override end to end: disposition the finding not_a_secret, see it listed in
// the project's allowlist, and see a re-execute of the same content go
// through instead of being denied again.
func TestNotASecretDispositionAllowlistsAndUnblocks(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID, f := executeToSecretSweepDenial(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}

	resp := doReq(t, http.MethodPost,
		ts.URL+"/api/v1/projects/"+projectID+"/findings/"+f.ID+"/disposition", token, "",
		`{"disposition":"not_a_secret"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disposition not_a_secret got %d: %s", resp.StatusCode, errorBody(t, resp))
	} else {
		resp.Body.Close()
	}

	// Listed in the project's allowlist: fingerprint prefix, kind, path, actor,
	// created_at.
	resp = doReq(t, http.MethodGet, ts.URL+"/api/v1/projects/"+projectID+"/secret-allowlist", token, "", "")
	var entries []secretAllowlistEntryView
	decode(t, resp, &entries)
	if len(entries) != 1 {
		t.Fatalf("expected 1 allowlist entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Kind != "AWS access key ID" {
		t.Errorf("Kind = %q, want %q", e.Kind, "AWS access key ID")
	}
	if e.Actor != "human" {
		t.Errorf("Actor = %q, want %q", e.Actor, "human")
	}
	if !strings.HasPrefix(f.SecretFingerprint, e.FingerprintPrefix) {
		t.Errorf("FingerprintPrefix %q is not a prefix of the finding's fingerprint %q", e.FingerprintPrefix, f.SecretFingerprint)
	}
	if len(e.FingerprintPrefix) == len(f.SecretFingerprint) {
		t.Errorf("expected a PREFIX, not the full fingerprint: %q", e.FingerprintPrefix)
	}

	// Re-run the same task: the identical content now passes the sweep.
	opID, err := rt.ExecuteInitiative(t.Context(), projectID, iniID)
	if err != nil {
		t.Fatalf("re-execute: %v", err)
	}
	waitForOperation(t, ts, token, opID)
	sets, err := pr.db.ListChangeSets(t.Context(), iniID)
	if err != nil || len(sets) != 2 {
		t.Fatalf("expected 2 change sets after the re-run, got %d (err=%v)", len(sets), err)
	}
	if sets[1].Status == domain.ChangeSetRejected {
		t.Errorf("the allowlisted fingerprint still denied the re-run: %+v", sets[1])
	}

	// Revoke: the sweep denies it again.
	resp = doReq(t, http.MethodDelete, ts.URL+"/api/v1/projects/"+projectID+"/secret-allowlist/"+e.ID, token, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke got %d: %s", resp.StatusCode, errorBody(t, resp))
	} else {
		resp.Body.Close()
	}
	resp = doReq(t, http.MethodGet, ts.URL+"/api/v1/projects/"+projectID+"/secret-allowlist", token, "", "")
	var afterRevoke []secretAllowlistEntryView
	decode(t, resp, &afterRevoke)
	if len(afterRevoke) != 0 {
		t.Fatalf("expected the allowlist empty after revoke, got %d", len(afterRevoke))
	}
}

// TestNotASecretRefusedOnNonSecretSweepFinding is the disposition's category
// scoping (review changes #1): every finding except a secret_sweep one
// refuses not_a_secret with 400, so the one disposition that also writes a
// project-wide allowlist entry cannot be forged onto an unrelated finding.
func TestNotASecretRefusedOnNonSecretSweepFinding(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	f := &domain.Finding{
		InitiativeID: iniID, TargetType: "change_set", TargetID: "CHG-1",
		Severity: domain.SeverityHigh, Confidence: domain.ConfidenceHigh,
		Category: "quality", Finding: "unrelated finding", Recommendation: "r",
	}
	if err := pr.db.CreateFinding(t.Context(), f); err != nil {
		t.Fatal(err)
	}

	resp := doReq(t, http.MethodPost,
		ts.URL+"/api/v1/projects/"+projectID+"/findings/"+f.ID+"/disposition", token, "",
		`{"disposition":"not_a_secret"}`)
	body := errorBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("not_a_secret on a %q finding got %d, want 400: %s", f.Category, resp.StatusCode, body)
	}
	if !strings.Contains(body, "secret_sweep") {
		t.Errorf("the refusal should name the required category: %q", body)
	}

	// No allowlist entry was written on the refused attempt.
	resp = doReq(t, http.MethodGet, ts.URL+"/api/v1/projects/"+projectID+"/secret-allowlist", token, "", "")
	var entries []secretAllowlistEntryView
	decode(t, resp, &entries)
	if len(entries) != 0 {
		t.Fatalf("expected no allowlist entry from a refused disposition, got %d", len(entries))
	}
}
