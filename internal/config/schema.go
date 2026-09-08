package config

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
    "demo": {
      "type": "object",
      "properties": {
        "block_review_cycles": { "type": "integer", "minimum": 0 }
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
