package daemon

import (
	"net/http"
	"sort"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/executor"
)

// ExecutorConfigView is GET .../executors/config: the whole executor block as
// harness.yaml holds it, for the Settings editor -- GET .../executors lists
// kinds and readiness but only the kind/image/model fields of the config.
type ExecutorConfigView struct {
	Config config.ExecutorConfig `json:"config"`
	// Compat is the executor/model compatibility advisory for the kind and
	// model asked about (?kind=&model=, defaulting to the saved ones):
	// blocked (known to fail every run, refused at execute) or untested.
	// Null when the combination is verified or there is nothing to say.
	Compat *ExecutorCompat `json:"compat"`
	// Env maps every environment variable the executor block names -- the
	// OpenHands session key, each MCP value_env -- to whether it is set in
	// the daemon's environment or the project's .env. Never a value.
	Env map[string]bool `json:"env"`
	// Problems are the configuration findings about the executor block.
	Problems []config.Problem `json:"problems"`
}

// ExecutorCompat is one compatibility advisory.
type ExecutorCompat struct {
	Kind    string `json:"kind"`
	Model   string `json:"model"`
	Blocked bool   `json:"blocked"`
	Message string `json:"message"`
}

func (s *Server) handleExecutorConfig(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	h := pr.cfg.Harness
	view := ExecutorConfigView{Config: h.Executor, Env: map[string]bool{}, Problems: []config.Problem{}}
	if view.Config.MCPServers == nil {
		view.Config.MCPServers = []config.MCPServerConfig{}
	}

	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		kind = string(h.Executor.Kind)
	}
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	if model == "" {
		model = h.Executor.Model
	}
	if model == "" {
		model = h.Models["implementer"]
	}
	if model == "" {
		model = h.DefaultModel
	}
	if c := executor.CheckModelCompat(kind, model); c.Message != "" {
		view.Compat = &ExecutorCompat{Kind: kind, Model: model, Blocked: c.Blocked, Message: c.Message}
	}

	if h.Executor.Kind == config.ExecutorOpenHands || h.Executor.OpenHands.URL != "" {
		name := h.Executor.OpenHands.EffectiveAPIKeyEnv()
		view.Env[name] = lookupEnv(name, pr.dotEnv) != ""
	}
	for _, m := range h.Executor.MCPServers {
		for _, nv := range append(append([]config.MCPNameValue{}, m.Env...), m.Headers...) {
			if nv.ValueEnv != "" {
				view.Env[nv.ValueEnv] = lookupEnv(nv.ValueEnv, pr.dotEnv) != ""
			}
		}
	}
	for _, p := range pr.cfg.ProblemsWithEnv(pr.dotEnv) {
		if strings.HasPrefix(p.Field, "harness.executor") {
			view.Problems = append(view.Problems, p)
		}
	}
	sort.SliceStable(view.Problems, func(i, j int) bool { return view.Problems[i].Field < view.Problems[j].Field })
	writeJSON(w, http.StatusOK, view)
}
