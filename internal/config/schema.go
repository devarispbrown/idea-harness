package config

import (
	"encoding/json"
	"sort"
)

// Embedded JSON Schemas for the portable config files (§28). These are the
// single source of truth; `idea config validate` checks against them.

const projectSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "project.yaml",
  "type": "object",
  "properties": {
    "name": { "type": "string" }
  },
  "additionalProperties": false
}`

const harnessSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "harness.yaml",
  "type": "object",
  "properties": {
    "network": { "enum": ["disabled", "enabled"] },
    "review": {
      "type": "object",
      "properties": {
        "max_cycles":   { "type": "integer", "minimum": 0 },
        "max_tokens":   { "type": "integer", "minimum": 0 },
        "max_cost_usd": { "type": "number",  "minimum": 0 }
      },
      "additionalProperties": false
    },
    "research": {
      "type": "object",
      "properties": {
        "urls": { "type": "array", "items": { "type": "string" } }
      },
      "additionalProperties": false
    },
    "providers": { "type": "object" },
    "default_model": { "type": "string" },
    "models": { "type": "object" },
    "build_cmd": { "type": "string" },
    "test_cmd": { "type": "string" },
    "check_timeout_seconds": { "type": "integer", "minimum": 0 },
    "git_commit": { "type": "boolean" },
    "git": {
      "type": "object",
      "properties": {
        "allow_default_branch":  { "type": "boolean" },
        "branch_per_initiative": { "type": "boolean" }
      },
      "additionalProperties": false
    },
    "demo": {
      "type": "object",
      "properties": {
        "block_review_cycles": { "type": "integer", "minimum": 0 },
        "block_plan_review_cycles": { "type": "integer", "minimum": 0 },
        "block_change_set_review_cycles": { "type": "integer", "minimum": 0 },
        "dangle_plan_dependency": { "type": "boolean" }
      },
      "additionalProperties": false
    },
    "executor": {
      "type": "object",
      "properties": {
        "kind":  { "enum": ["internal", "container", "omp", "hermes"] },
        "image": { "type": "string" },
        "model": { "type": "string" }
      },
      "additionalProperties": false
    },
    "check": {
      "type": "object",
      "properties": {
        "image": { "type": "string" }
      },
      "additionalProperties": false
    }
  },
  "additionalProperties": false
}`

// HarnessTopLevelKeys returns the field names harness.yaml accepts at its top
// level, from the schema that validates it.
//
// This exists so advice about harness.yaml can be checked against harness.yaml.
// `idea reviews` recommended `set tests_cmd in harness.yaml`, built by
// concatenating a finding category with "_cmd"; the field is `test_cmd`, and
// following the advice literally made `idea config validate` exit 1 with
// `additional properties 'tests_cmd' not allowed`. Nothing tied the prose to
// the schema, so nothing caught it.
func HarnessTopLevelKeys() []string {
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(harnessSchema), &doc); err != nil {
		return nil
	}
	out := make([]string, 0, len(doc.Properties))
	for k := range doc.Properties {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
