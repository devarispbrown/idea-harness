package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/executor"
)

// TestUpdateExecutorsStoresThePinnedDefaultImage is C1: PUT with kind hermes
// and no image must store executor.DefaultAgentImage("hermes"), not leave the
// image empty for the runtime to fall back to alpine:3.20 -- alpine has no
// agent CLI, and that empty-image fallback was the actual cause of the exit
// 127 the audit reported.
func TestUpdateExecutorsStoresThePinnedDefaultImage(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)

	resp := doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/executors",
		token, "", `{"kind":"hermes"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}

	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := config.Load(filepath.Join(pr.rootPath, config.Dir))
	if err != nil {
		t.Fatal(err)
	}
	want := executor.DefaultAgentImage("hermes")
	if after.Harness.Executor.Image != want {
		t.Fatalf("stored image = %q, want the pinned default %q", after.Harness.Executor.Image, want)
	}
}

// TestUpdateExecutorsLeavesAnExplicitImageAlone is the guard's counterpart: a
// caller-supplied image must not be overwritten by the default.
func TestUpdateExecutorsLeavesAnExplicitImageAlone(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)

	resp := doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/executors",
		token, "", `{"kind":"hermes","image":"my-registry/custom-hermes:v1"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}

	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := config.Load(filepath.Join(pr.rootPath, config.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if after.Harness.Executor.Image != "my-registry/custom-hermes:v1" {
		t.Fatalf("stored image = %q, want the caller's own image", after.Harness.Executor.Image)
	}
}

// TestUpdateExecutorsRejectsAnUnresolvableModel is C2: a model that cannot
// resolve through executor.ResolveAgentProvider must 400 with the reason,
// rather than being written to harness.yaml and failing later inside the
// agent container -- far from the settings page that caused it.
func TestUpdateExecutorsRejectsAnUnresolvableModel(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)

	resp := doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/executors",
		token, "", `{"kind":"omp","model":"typo-provider/some-model"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, b)
	}
	var out struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Error == "" {
		t.Fatal("400 carries no reason")
	}

	// The rejected write must not land on disk.
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := config.Load(filepath.Join(pr.rootPath, config.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if after.Harness.Executor.Model == "typo-provider/some-model" {
		t.Fatal("an unresolvable model was written to harness.yaml despite the 400")
	}
}

// TestUpdateExecutorsAcceptsAResolvableModel is C2's positive case: a model
// that DOES resolve (a built-in preset, or a providers: entry) must save.
func TestUpdateExecutorsAcceptsAResolvableModel(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)

	resp := doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/executors",
		token, "", `{"kind":"hermes","model":"anthropic/claude-sonnet-4-5-20250929"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}

	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := config.Load(filepath.Join(pr.rootPath, config.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if after.Harness.Executor.Model != "anthropic/claude-sonnet-4-5-20250929" {
		t.Fatalf("stored model = %q", after.Harness.Executor.Model)
	}
}

// TestUpdateExecutorsSkipsModelValidationForInternal: C2 is only meaningful
// for hermes/omp, which are the kinds that actually forward the model into an
// agent container. The internal/container kinds do not read executor.model at
// all, so an unresolvable value there must not be refused.
func TestUpdateExecutorsSkipsModelValidationForInternal(t *testing.T) {
	_, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)

	resp := doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/executors",
		token, "", `{"kind":"internal","model":"typo-provider/some-model"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
}
