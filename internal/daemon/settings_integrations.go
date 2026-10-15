package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/security"
)

// handleUpdateIntegration writes one integration's harness.yaml section
// (PUT .../integrations/{name}): {"config": {...}} replaces it whole,
// {"config": null} removes it, which turns the integration off.
//
// Tokens never pass through here. The section names the environment
// variables that hold them (bot_token_env, app_token_env, api_key_env), and
// the status endpoint reports whether each is set -- never a value. A value
// that looks like a credential anywhere in the section is refused, so a
// token pasted into the wrong field is not written to a committable file.
func (s *Server) handleUpdateIntegration(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	name := r.PathValue("name")
	if name != "slack" && name != "linear" {
		writeError(w, http.StatusNotFound, fmt.Sprintf("no integration matching %q (use slack or linear)", name))
		return
	}
	var body struct {
		Config json.RawMessage `json:"config"`
	}
	if err := decodeStrict(r.Body, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body.Config) == 0 {
		writeError(w, http.StatusBadRequest, "config is required: the integration's section, or null to remove it")
		return
	}
	remove := bytes.Equal(bytes.TrimSpace(body.Config), []byte("null"))
	var slackCfg *config.SlackConfig
	var linearCfg *config.LinearConfig
	if !remove {
		var target any
		if name == "slack" {
			slackCfg = &config.SlackConfig{}
			target = slackCfg
		} else {
			linearCfg = &config.LinearConfig{}
			target = linearCfg
		}
		if err := decodeStrict(bytes.NewReader(body.Config), target); err != nil {
			writeError(w, http.StatusBadRequest, "config: "+err.Error())
			return
		}
		if field := credentialLookingField(target); field != "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("integrations.%s.%s looks like it contains a credential; tokens belong in the environment or the project's .env, named by the *_env fields (§47)", name, field))
			return
		}
	}
	if !s.writeHarness(w, r, pr, func(h *config.HarnessConfig) error {
		if name == "slack" {
			h.Integrations.Slack = slackCfg
		} else {
			h.Integrations.Linear = linearCfg
		}
		return nil
	}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "updated", "name": name, "removed": remove})
}

// credentialLookingField returns the json name of the first string field (or
// string-slice element) in v that security.FindCredential flags, or "".
func credentialLookingField(v any) string {
	rv := reflect.Indirect(reflect.ValueOf(v))
	return credentialField(rv, "")
}

func credentialField(rv reflect.Value, prefix string) string {
	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			return ""
		}
		return credentialField(rv.Elem(), prefix)
	case reflect.Struct:
		t := rv.Type()
		for i := 0; i < t.NumField(); i++ {
			tag := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
			if tag == "" || tag == "-" {
				continue
			}
			name := tag
			if prefix != "" {
				name = prefix + "." + tag
			}
			if f := credentialField(rv.Field(i), name); f != "" {
				return f
			}
		}
	case reflect.Slice:
		for i := 0; i < rv.Len(); i++ {
			if f := credentialField(rv.Index(i), fmt.Sprintf("%s[%d]", prefix, i)); f != "" {
				return f
			}
		}
	case reflect.String:
		if security.FindCredential(rv.String()) != "" {
			return prefix
		}
	}
	return ""
}
