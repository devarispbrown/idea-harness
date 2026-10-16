package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// PUT /executors configures openhands: a server URL is required, and one that
// would carry the work root and keys in the clear to another machine is
// refused before anything is written. GET /executors then reports the kind
// as remote, with its server on the resolution.
func TestUpdateExecutorsOpenHands(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)
	put := func(body string) (int, string) {
		resp := doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/executors", token, "", body)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := put(`{"kind":"openhands"}`); code != http.StatusBadRequest || !strings.Contains(body, "openhands_url") {
		t.Fatalf("no url: %d %s", code, body)
	}
	if code, body := put(`{"kind":"openhands","openhands_url":"http://agents.example.com"}`); code != http.StatusBadRequest || !strings.Contains(body, "https") {
		t.Fatalf("plain http: %d %s", code, body)
	}
	if code, body := put(`{"kind":"openhands","openhands_url":"http://127.0.0.1:8000","openhands_api_key_env":"OH_KEY"}`); code != http.StatusOK {
		t.Fatalf("loopback: %d %s", code, body)
	}
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := config.Load(filepath.Join(pr.rootPath, config.Dir))
	if err != nil {
		t.Fatal(err)
	}
	oh := after.Harness.Executor.OpenHands
	if after.Harness.Executor.Kind != config.ExecutorOpenHands || oh.URL != "http://127.0.0.1:8000" || oh.APIKeyEnv != "OH_KEY" || after.Harness.Executor.Image != "" {
		t.Fatalf("stored executor = %+v", after.Harness.Executor)
	}

	resp := doReq(t, http.MethodGet, ts.URL+"/api/v1/projects/"+projectID+"/executors", token, "", "")
	defer resp.Body.Close()
	var rows []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	for _, r := range rows {
		if r["name"] == "openhands" {
			row = r
		}
	}
	if row == nil || row["selected"] != true || row["trust"] != "remote_external" || row["sandbox_mode"] != "remote" {
		t.Fatalf("openhands row = %v", row)
	}
	res, _ := row["resolution"].(map[string]any)
	if res["server"] != "http://127.0.0.1:8000" {
		t.Fatalf("resolution = %v", res)
	}
	status, _ := row["status"].(map[string]any)
	if status["key_env"] != "OH_KEY" {
		t.Fatalf("status = %v", status)
	}
}

// Shutdown lets go of remote runs without stopping them: the supervisor is
// detached before operations are cancelled, so an openhands executor's
// Cancel leaves its run going on the server for the next daemon.
func TestShutdownDetachesRemoteRuns(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	sup := pr.engine.RemoteSupervisor()
	rt.beginShutdown()
	if !sup.Detached() {
		t.Fatal("shutdown did not detach the project's remote runs")
	}
}
