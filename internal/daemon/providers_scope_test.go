package daemon

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// TestUpdateProvidersRejectsOnlyWhatItWrites covers the lockout: a project whose
// harness.yaml already carries an unrelated error (a models.<role> naming an
// unknown provider) could not run `idea model set` or save UI Settings at all,
// because the handler rejected on any error anywhere in the candidate config —
// including the very command that would have fixed it.
func TestUpdateProvidersRejectsOnlyWhatItWrites(t *testing.T) {
	cases := []struct {
		name string
		// preModels is written to harness.yaml BEFORE the request, standing in for
		// a hand-edited / committed / restored config.
		preModels  map[string]string
		body       string
		wantStatus int
		// State expected on disk afterwards.
		wantDefaultModel string
		wantCritic       string
	}{
		{
			name:             "pre-existing unrelated error does not block a default_model write",
			preModels:        map[string]string{"critic": "bogus/model"},
			body:             `{"default_model":"openai/gpt-4o-mini"}`,
			wantStatus:       http.StatusOK,
			wantDefaultModel: "openai/gpt-4o-mini",
			wantCritic:       "bogus/model", // reported elsewhere, not silently rewritten
		},
		{
			name:             "pre-existing unrelated error does not block a provider write",
			preModels:        map[string]string{"critic": "bogus/model"},
			body:             `{"providers":{"groq":{"type":"openai","api_key_env":"GROQ_API_KEY","base_url":"https://api.groq.com/openai/v1"}}}`,
			wantStatus:       http.StatusOK,
			wantDefaultModel: "",
			wantCritic:       "bogus/model",
		},
		{
			name:             "the write that fixes the pre-existing error is allowed",
			preModels:        map[string]string{"critic": "bogus/model"},
			body:             `{"models":{"critic":"openai/gpt-4o-mini"}}`,
			wantStatus:       http.StatusOK,
			wantDefaultModel: "",
			wantCritic:       "openai/gpt-4o-mini",
		},
		{
			name:             "an error in a role this request writes is still rejected",
			preModels:        map[string]string{"critic": "bogus/model"},
			body:             `{"models":{"reviewer":"alsobogus/model"}}`,
			wantStatus:       http.StatusBadRequest,
			wantDefaultModel: "",
			wantCritic:       "bogus/model",
		},
		{
			name:             "an unknown provider in default_model is still rejected",
			body:             `{"default_model":"bogus/model"}`,
			wantStatus:       http.StatusBadRequest,
			wantDefaultModel: "",
		},
		{
			name:             "a preset base_url override is still rejected",
			body:             `{"providers":{"openai":{"base_url":"https://attacker.tld/v1"}}}`,
			wantStatus:       http.StatusBadRequest,
			wantDefaultModel: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rt, ts, token := newTestServer(t)
			projectID := newTestProject(t, ts.URL, token)

			pr, err := rt.EnsureProject(t.Context(), projectID)
			if err != nil {
				t.Fatal(err)
			}
			cfgDir := filepath.Join(pr.rootPath, config.Dir)

			if c.preModels != nil {
				h := pr.cfg.Harness
				h.Models = c.preModels
				if err := config.WriteHarness(cfgDir, h); err != nil {
					t.Fatal(err)
				}
				if _, err := rt.ReloadProject(t.Context(), projectID); err != nil {
					t.Fatal(err)
				}
			}

			resp := doReq(t, http.MethodPut, ts.URL+"/api/v1/projects/"+projectID+"/providers", token, "", c.body)
			defer resp.Body.Close()
			if resp.StatusCode != c.wantStatus {
				t.Fatalf("status: got %d, want %d", resp.StatusCode, c.wantStatus)
			}

			cfg, err := config.Load(cfgDir)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Harness.DefaultModel != c.wantDefaultModel {
				t.Fatalf("default_model on disk: got %q, want %q", cfg.Harness.DefaultModel, c.wantDefaultModel)
			}
			if got := cfg.Harness.Models["critic"]; got != c.wantCritic {
				t.Fatalf("models.critic on disk: got %q, want %q", got, c.wantCritic)
			}
		})
	}
}

// TestProblemAttribution pins the attribution rule the handler uses: a problem
// belongs to this request when its harness.yaml field path is one the body
// wrote, or lives under it.
func TestProblemAttribution(t *testing.T) {
	touched := touchedConfigFields(
		"openai/gpt-4o-mini",
		map[string]config.ProviderConfig{"groq": {}},
		map[string]string{"critic": "openai/gpt-4o-mini"},
	)

	cases := []struct {
		name  string
		field string
		want  bool
	}{
		{"default_model this request set", "harness.default_model", true},
		{"provider entry this request wrote", "harness.providers.groq", true},
		{"sub-field of a provider this request wrote", "harness.providers.groq.base_url", true},
		{"role this request wrote", "harness.models.critic", true},
		{"provider this request did not touch", "harness.providers.openai", false},
		{"role this request did not touch", "harness.models.reviewer", false},
		{"executor model, which this endpoint cannot write", "harness.executor.model", false},
		{"prefix that is not a path boundary", "harness.default_model_extra", false},
		{"prefix that is not a path boundary on a provider", "harness.providers.groq2", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := problemIsAttributable(config.Problem{Severity: config.SeverityError, Field: c.field}, touched)
			if got != c.want {
				t.Fatalf("attributable(%q): got %v, want %v", c.field, got, c.want)
			}
		})
	}

	// An empty body owns nothing: a write that sets only git_commit must not be
	// blamed for any model or provider problem.
	empty := touchedConfigFields("", nil, nil)
	for _, field := range []string{"harness.default_model", "harness.providers.groq", "harness.models.critic"} {
		if problemIsAttributable(config.Problem{Field: field}, empty) {
			t.Fatalf("a body that writes no model/provider fields must not own %q", field)
		}
	}
}

// TestKeySetIsIndependentOfReady pins the distinction `idea doctor` needs.
//
// `ready` folds three facts together — the entry was refused, the provider needs
// no key, the key is missing — so a caller cannot tell "no key" from "this entry
// was rejected". Those have different fixes: one is `idea daemon stop`, the
// other is editing harness.yaml, and telling someone to restart the daemon over
// a bad base_url names a fix that changes nothing.
func TestKeySetIsIndependentOfReady(t *testing.T) {
	cfg := config.Config{Harness: config.HarnessConfig{
		DefaultModel: "openai/gpt-4o-mini",
		Providers: map[string]config.ProviderConfig{
			// A preset whose base_url may not be overridden: refused, but the
			// key is right there.
			"openai": {BaseURL: "https://proxy.example.invalid/v1"},
		},
	}}
	dotEnv := map[string]string{"OPENAI_API_KEY": "sk-present"}

	view := buildProvidersView(t.Context(), cfg, dotEnv, nil)
	var openai *providerSummary
	for i := range view.Providers {
		if view.Providers[i].Name == "openai" {
			openai = &view.Providers[i]
		}
	}
	if openai == nil {
		t.Fatal("openai missing from the view")
	}
	if openai.Problem == "" {
		t.Fatal("expected the overridden base_url to be refused; the fixture no longer reproduces")
	}
	if openai.Ready {
		t.Error("a refused entry reported ready")
	}
	if !openai.KeySet {
		t.Error("key_set was false though the key is present; doctor cannot then tell " +
			"a missing key from a refused entry, and would name the wrong fix")
	}
}
