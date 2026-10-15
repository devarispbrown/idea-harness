package config

import (
	"encoding/json"
	"sort"
	"strings"
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

// integrationEventEnum is IntegrationEventKinds rendered as a JSON enum body,
// spliced into the schema so the vocabulary is written down once.
var integrationEventEnum = func() string {
	q := make([]string, len(IntegrationEventKinds))
	for i, k := range IntegrationEventKinds {
		q[i] = `"` + k + `"`
	}
	return strings.Join(q, ", ")
}()

var harnessSchema = `{
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
        "max_cost_usd": { "type": "number",  "minimum": 0 },
        "rigor":        { "enum": ["auto", "quick", "standard", "thorough"] },
        "convergence": {
          "type": "object",
          "properties": {
            "mode":             { "enum": ["guarded", "legacy"] },
            "gate_severity":    { "enum": ["high", "critical"] },
            "require_citation": { "type": "boolean" },
            "later_findings":   { "enum": ["changed", "critical", "any"] },
            "stall_cycles":     { "type": "integer", "minimum": 0 }
          },
          "additionalProperties": false
        }
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
        "kind":  { "enum": ["auto", "internal", "container", "omp", "hermes", "acp"] },
        "image": { "type": "string" },
        "model": { "type": "string" },
        "acp": {
          "type": "object",
          "properties": {
            "command":         { "type": "array", "items": { "type": "string" } },
            "key_env":         { "type": "string" },
            "timeout_seconds": { "type": "integer", "minimum": 0 }
          },
          "additionalProperties": false
        },
        "mcp_servers": {
          "type": "array",
          "items": {
            "type": "object",
            "required": ["name"],
            "properties": {
              "name":    { "type": "string" },
              "type":    { "enum": ["stdio", "http", "sse"] },
              "command": { "type": "string" },
              "args":    { "type": "array", "items": { "type": "string" } },
              "url":     { "type": "string" },
              "env":     { "$ref": "#/$defs/nameValues" },
              "headers": { "$ref": "#/$defs/nameValues" }
            },
            "additionalProperties": false
          }
        }
      },
      "additionalProperties": false
    },
    "check": {
      "type": "object",
      "properties": {
        "image": { "type": "string" }
      },
      "additionalProperties": false
    },
    "verify": {
      "type": "object",
      "properties": {
        "dimensions": {
          "type": "object",
          "properties": {
            "setup_reproducibility": { "$ref": "#/$defs/qualityMode" },
            "docs_completeness":     { "$ref": "#/$defs/qualityMode" },
            "api_compatibility":     { "$ref": "#/$defs/qualityMode" },
            "api_ergonomics":        { "$ref": "#/$defs/qualityMode" },
            "security":              { "$ref": "#/$defs/qualityMode" },
            "performance":           { "$ref": "#/$defs/qualityMode" },
            "operational_readiness": { "$ref": "#/$defs/qualityMode" }
          },
          "additionalProperties": false
        },
        "setup": {
          "type": "object",
          "properties": {
            "commands": { "type": "array", "items": { "type": "string", "minLength": 1 } },
            "prepare":  { "type": "array", "items": { "type": "string", "minLength": 1 } }
          },
          "additionalProperties": false
        },
        "security": {
          "type": "object",
          "properties": {
            "scanners": {
              "type": "array",
              "items": {
                "type": "object",
                "required": ["name", "command"],
                "properties": {
                  "name":    { "type": "string", "minLength": 1 },
                  "command": { "type": "string", "minLength": 1 },
                  "network": { "type": "boolean" }
                },
                "additionalProperties": false
              }
            }
          },
          "additionalProperties": false
        },
        "performance": {
          "type": "object",
          "properties": {
            "budgets": {
              "type": "array",
              "items": {
                "type": "object",
                "required": ["name", "command", "metric"],
                "properties": {
                  "name":               { "type": "string", "minLength": 1 },
                  "command":            { "type": "string", "minLength": 1 },
                  "metric":             { "type": "string", "minLength": 1 },
                  "unit":               { "type": "string" },
                  "max":                { "type": "number" },
                  "max_regression_pct": { "type": "number", "minimum": 0 }
                },
                "additionalProperties": false
              }
            }
          },
          "additionalProperties": false
        },
        "model_review": { "type": "array", "items": { "enum": ["docs_completeness", "operational_readiness"] } },
        "timeout_seconds": { "type": "integer", "minimum": 0 }
      },
      "additionalProperties": false
    },
    "integrations": {
      "type": "object",
      "properties": {
        "slack": {
          "type": "object",
          "properties": {
            "channel":             { "type": "string" },
            "events":              { "type": "array", "items": { "enum": [` + integrationEventEnum + `] } },
            "mention_on_judgment": { "type": "array", "items": { "type": "string" } },
            "allowed_users":       { "type": "array", "items": { "type": "string" } },
            "socket_mode":         { "type": "boolean" },
            "bot_token_env":       { "type": "string" },
            "app_token_env":       { "type": "string" },
            "ui_base_url":         { "type": "string" }
          },
          "required": ["channel"],
          "additionalProperties": false
        },
        "linear": {
          "type": "object",
          "properties": {
            "team_key":              { "type": "string" },
            "project":               { "type": "string" },
            "inbound_label":         { "type": "string" },
            "inbound":               { "type": "boolean" },
            "poll_interval_seconds": { "type": "integer", "minimum": 0 },
            "api_key_env":           { "type": "string" },
            "state_mapping": {
              "type": "object",
              "properties": {
                "todo":        { "type": "string" },
                "in_progress": { "type": "string" },
                "in_review":   { "type": "string" },
                "done":        { "type": "string" },
                "canceled":    { "type": "string" }
              },
              "additionalProperties": false
            }
          },
          "required": ["team_key"],
          "additionalProperties": false
        }
      },
      "additionalProperties": false
    }
  },
  "additionalProperties": false,
  "$defs": {
    "qualityMode": { "enum": ["off", "advisory", "gate"] },
    "nameValues": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["name"],
        "properties": {
          "name":      { "type": "string" },
          "value":     { "type": "string" },
          "value_env": { "type": "string" }
        },
        "additionalProperties": false
      }
    }
  }
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
