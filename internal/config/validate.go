package config

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// secretKeyRe matches config keys that would carry secret VALUES. Keys ending
// in "_env" reference an environment variable name and are allowed.
var secretKeyRe = regexp.MustCompile(`(?i)(secret|password|passwd|api_?key|token|credential|private_?key|bearer)`)

// validateAgainst validates a raw document (already JSON-compatible) against an
// embedded JSON Schema.
func validateAgainst(name, schemaJSON string, doc any) error {
	schemaDoc, err := jsonschema.UnmarshalJSON(strings.NewReader(schemaJSON))
	if err != nil {
		return fmt.Errorf("config: compile schema %s: %w", name, err)
	}
	compiler := jsonschema.NewCompiler()
	url := "https://idea-harness.local/schemas/" + name
	if err := compiler.AddResource(url, schemaDoc); err != nil {
		return fmt.Errorf("config: add schema %s: %w", name, err)
	}
	sch, err := compiler.Compile(url)
	if err != nil {
		return fmt.Errorf("config: compile schema %s: %w", name, err)
	}
	if err := sch.Validate(doc); err != nil {
		// Typed for the same reason a syntax error is: this is the user's file,
		// and it is the MORE likely malformation — a valid-YAML document the
		// schema rejects, `tests_cmd` being the example this project shipped
		// advice for. Untyped, it reached every project-scoped route as a 404
		// carrying raw jsonschema text.
		return &ParseError{File: name, Msg: "invalid: " + err.Error()}
	}
	return nil
}

// rejectSecrets walks the portable config and rejects any key that would store
// a secret value (§47). *_env references are permitted.
func rejectSecrets(path string, node any) error {
	switch v := node.(type) {
	case map[string]any:
		for k, val := range v {
			key := strings.ToLower(k)
			if secretKeyRe.MatchString(key) && !strings.HasSuffix(key, "_env") {
				if s, ok := val.(string); ok && s != "" {
					return fmt.Errorf("config: %s must not contain an inline secret; use an *_env reference (§47)", joinPath(path, k))
				}
			}
			if err := rejectSecrets(joinPath(path, k), val); err != nil {
				return err
			}
		}
	case []any:
		for i, item := range v {
			if err := rejectSecrets(fmt.Sprintf("%s[%d]", path, i), item); err != nil {
				return err
			}
		}
	}
	return nil
}

func joinPath(base, key string) string {
	if base == "" {
		return key
	}
	return base + "." + key
}
