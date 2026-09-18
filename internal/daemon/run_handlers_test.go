package daemon

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// smokeDocker is a fake docker for the smoke test: when $FAKE_WRITES is set
// the agent writes a file into its /work mount, as a working agent does.
const smokeDocker = `#!/usr/bin/env bash
case "$1" in
  run)
    prev=""
    for a in "$@"; do
      if [ "$prev" = "-v" ] && [ "${a%:/work}" != "$a" ]; then work="${a%:/work}"; fi
      prev="$a"
    done
    [ -n "$FAKE_WRITES" ] && echo "smoke ok" > "$work/smoke.txt"
    [ -n "$FAKE_SLOW" ] && sleep 2
    echo "cid-smoke" ;;
  inspect) echo "exited 0" ;;
  logs) echo "Error: HTTP 401 invalid key" ;;
  rm|image) exit 0 ;;
  *) exit 1 ;;
esac
`

func smokeProject(t *testing.T) (*Runtime, string, string, string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(smokeDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENAI_API_KEY", "sk-test-smoke")
	rt, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)
	resp := doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/executors", token, "",
		`{"kind":"hermes","model":"openai/gpt-5.6-luna"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("configure hermes: %d", resp.StatusCode)
	}
	return rt, ts.URL, token, projectID
}

func startSmoke(t *testing.T, base, token, projectID string) (int, string) {
	t.Helper()
	resp := doReq(t, http.MethodPost, base+"/api/v1/projects/"+projectID+"/executors/smoke", token, "", `{}`)
	defer resp.Body.Close()
	var body struct {
		OperationID string `json:"operation_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body.OperationID
}

func waitOperation(t *testing.T, base, token, opID string) domain.Operation {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp := doReq(t, http.MethodGet, base+"/api/v1/operations/"+opID, token, "", "")
		var op domain.Operation
		_ = json.NewDecoder(resp.Body).Decode(&op)
		resp.Body.Close()
		if op.Status == domain.OperationSucceeded || op.Status == domain.OperationFailed || op.Status == domain.OperationCancelled {
			return op
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the smoke operation never finished")
	return domain.Operation{}
}

func TestSmokeFromTheDaemonPassesAndIsRemembered(t *testing.T) {
	t.Setenv("FAKE_WRITES", "1")
	_, base, token, projectID := smokeProject(t)
	code, opID := startSmoke(t, base, token, projectID)
	if code != http.StatusAccepted || opID == "" {
		t.Fatalf("POST smoke = %d %q", code, opID)
	}
	if op := waitOperation(t, base, token, opID); op.Status != domain.OperationSucceeded {
		t.Fatalf("a working agent's smoke %s: %s", op.Status, op.Error)
	}
	// The result survives a reload: GET /executors carries it.
	resp := doReq(t, http.MethodGet, base+"/api/v1/projects/"+projectID+"/executors", token, "", "")
	defer resp.Body.Close()
	var list []struct {
		Name   string `json:"name"`
		Status struct {
			LastSmoke *smokeResult `json:"last_smoke"`
		} `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	for _, e := range list {
		if e.Name == "hermes" {
			if e.Status.LastSmoke == nil || e.Status.LastSmoke.Status != domain.OperationSucceeded {
				t.Errorf("last_smoke = %+v, want succeeded", e.Status.LastSmoke)
			}
			return
		}
	}
	t.Error("no hermes row")
}

// An agent that writes nothing fails the smoke, and the failure carries the
// agent's own output -- the reason.
func TestAFailedSmokeSaysWhy(t *testing.T) {
	_, base, token, projectID := smokeProject(t)
	_, opID := startSmoke(t, base, token, projectID)
	op := waitOperation(t, base, token, opID)
	if op.Status != domain.OperationFailed || !strings.Contains(op.Error, "401") {
		t.Errorf("smoke = %s %q, want failed with the agent's 401", op.Status, op.Error)
	}
}

func TestSmokeNeedsACodingAgent(t *testing.T) {
	_, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)
	if code, _ := startSmoke(t, ts.URL, token, projectID); code != http.StatusBadRequest {
		t.Errorf("smoke on the internal executor = %d, want 400", code)
	}
}

// Concurrent requests: exactly one smoke runs. A list-then-create check lets
// two through; the claim is taken atomically.
func TestOnlyOneSmokeRunsAtATime(t *testing.T) {
	t.Setenv("FAKE_SLOW", "1")
	_, base, token, projectID := smokeProject(t)
	var wg sync.WaitGroup
	codes := make([]int, 8)
	ops := make([]string, 8)
	for i := range codes {
		wg.Add(1)
		go func() { defer wg.Done(); codes[i], ops[i] = startSmoke(t, base, token, projectID) }()
	}
	wg.Wait()
	accepted := 0
	for i, c := range codes {
		switch c {
		case http.StatusAccepted:
			accepted++
			// Wait for it: returning while it runs let it outlive the test's
			// fake docker and reach the real one.
			waitOperation(t, base, token, ops[i])
		case http.StatusConflict:
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	if accepted != 1 {
		t.Errorf("%d smokes accepted concurrently, want exactly 1", accepted)
	}
}

func TestAgentOutputAndPartialWorkEndpoints(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	base := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID
	resp := doReq(t, http.MethodGet, base+"/agent-output", token, "", "")
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out["running"] != false || out["output"] != "" {
		t.Errorf("agent-output with nothing running = %v", out)
	}
	resp = doReq(t, http.MethodDelete, base+"/partial-work", token, "", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("DELETE partial-work with nothing kept = %d, want 200", resp.StatusCode)
	}
}

// "Last smoke test passed" describes the setup it tested. After the model
// changes it no longer applies, and is not shown.
func TestLastSmokeIsForTheCurrentSetupOnly(t *testing.T) {
	t.Setenv("FAKE_WRITES", "1")
	_, base, token, projectID := smokeProject(t)
	_, opID := startSmoke(t, base, token, projectID)
	waitOperation(t, base, token, opID)
	resp := doReq(t, http.MethodPut, base+"/api/v1/projects/"+projectID+"/executors", token, "",
		`{"model":"openai/gpt-5.6-sol"}`)
	resp.Body.Close()

	resp = doReq(t, http.MethodGet, base+"/api/v1/projects/"+projectID+"/executors", token, "", "")
	defer resp.Body.Close()
	var list []struct {
		Name   string `json:"name"`
		Status struct {
			LastSmoke *smokeResult `json:"last_smoke"`
		} `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	for _, e := range list {
		if e.Name == "hermes" && e.Status.LastSmoke != nil {
			t.Errorf("a smoke of the old model is reported for the new one: %+v", e.Status.LastSmoke)
		}
	}
}
