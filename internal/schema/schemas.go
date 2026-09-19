// Package schema embeds the structured-output JSON Schemas for each workflow
// stage's artifact (§39). These are the contract the model output must satisfy.
package schema

// IdeaBrief is the framing-stage output.
const IdeaBrief = `{
  "type": "object",
  "properties": {
    "title": {"type": "string"},
    "summary": {"type": "string"},
    "primary_user": {"type": "string"},
    "problem": {"type": "string"},
    "outcome": {"type": "string"},
    "constraints": {"type": "array", "items": {"type": "string"}},
    "assumptions": {"type": "array", "items": {"type": "string"}},
    "open_questions": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["title", "summary"],
  "additionalProperties": true
}`

// ResearchBrief is the research-stage output.
const ResearchBrief = `{
  "type": "object",
  "properties": {
    "summary": {"type": "string"},
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "statement": {"type": "string"},
          "type": {"enum": ["fact", "assumption", "hypothesis"]},
          "confidence": {"enum": ["high", "medium", "low"]},
          "source": {"type": "string"}
        },
        "required": ["statement", "type", "confidence", "source"]
      }
    }
  },
  "required": ["findings"],
  "additionalProperties": true
}`

// PRD is the product-definition-stage output.
const PRD = `{
  "type": "object",
  "properties": {
    "summary": {"type": "string"},
    "requirements": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "statement": {"type": "string"},
          "priority": {"enum": ["must", "should", "could"]},
          "decision": {"type": "string", "description": "Exact question text of the decision this requirement is shaped by, if any."},
          "acceptance_criteria": {
            "type": "array",
            "items": {
              "type": "object",
              "properties": {
                "metric": {"type": "string"},
                "target": {"type": "string"},
                "condition": {"type": "string"}
              },
              "required": ["metric", "target", "condition"]
            }
          }
        },
        "required": ["statement", "priority", "acceptance_criteria"]
      }
    },
    "decisions": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "question": {"type": "string"},
          "options": {"type": "array", "items": {"type": "string"}},
          "recommendation": {"type": "string"}
        },
        "required": ["question", "options", "recommendation"]
      }
    },
    "open_questions": {"type": "array", "items": {"type": "string"}},
    "risks": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["summary", "requirements"],
  "additionalProperties": true
}`

// Review is the adversarial-review-stage output (structured findings).
const Review = `{
  "type": "object",
  "properties": {
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "severity": {"enum": ["critical", "high", "medium", "low", "observation"]},
          "confidence": {"enum": ["high", "medium", "low"]},
          "category": {"type": "string"},
          "finding": {"type": "string"},
          "evidence": {"type": "string"},
          "impact": {"type": "string"},
          "recommendation": {"type": "string"},
          "path": {"type": "string", "description": "The file this finding is about, if it names a specific one (change-set review only; review B2)."},
          "in_diff": {"type": "boolean", "description": "Self-report: true if this finding is about code the change set actually touches. Only consulted when path is omitted (review B2)."}
        },
        "required": ["severity", "confidence", "category", "finding", "recommendation"]
      }
    }
  },
  "required": ["findings"],
  "additionalProperties": true
}`

// Discussion is the free-form response for a human↔AI discussion turn
// (decision elaboration / proposal evaluation).
const Discussion = `{
  "type": "object",
  "properties": {
    "response": {"type": "string"}
  },
  "required": ["response"],
  "additionalProperties": true
}`

// Architecture is the architecture-stage output (technical design).
const Architecture = `{
  "type": "object",
  "properties": {
    "summary": {"type": "string"},
    "components": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "name": {"type": "string"},
          "responsibility": {"type": "string"},
          "depends_on": {"type": "array", "items": {"type": "string"}}
        },
        "required": ["name", "responsibility"]
      }
    },
    "data_flow": {"type": "array", "items": {"type": "string"}},
    "decisions": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "question": {"type": "string"},
          "options": {"type": "array", "items": {"type": "string"}},
          "recommendation": {"type": "string"}
        },
        "required": ["question", "options", "recommendation"]
      }
    },
    "risks": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["summary", "components"],
  "additionalProperties": true
}`

// ProductDesign is the product-design-stage output (design spec).
const ProductDesign = `{
  "type": "object",
  "properties": {
    "summary": {"type": "string"},
    "flows": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "name": {"type": "string"},
          "goal": {"type": "string"},
          "steps": {"type": "array", "items": {"type": "string"}}
        },
        "required": ["name", "steps"]
      }
    },
    "screens": {"type": "array", "items": {"type": "string"}},
    "decisions": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "question": {"type": "string"},
          "options": {"type": "array", "items": {"type": "string"}},
          "recommendation": {"type": "string"}
        },
        "required": ["question", "options", "recommendation"]
      }
    },
    "risks": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["summary", "flows"],
  "additionalProperties": true
}`

// VerificationPlan is the verification-design-stage output.
const VerificationPlan = `{
  "type": "object",
  "properties": {
    "summary": {"type": "string"},
    "hooks": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "requirement": {"type": "string"},
          "evidence": {"type": "string"},
          "check": {"type": "string"},
          "tests": {"type": "array", "items": {"type": "string"}, "description": "Executable test names that cover this requirement's acceptance, if named (C2, wave 3 contract)."}
        },
        "required": ["requirement", "evidence", "check"]
      }
    }
  },
  "required": ["summary", "hooks"],
  "additionalProperties": true
}`

// ExecutionPlan is the execution-plan-stage output (ordered tasks).
const ExecutionPlan = `{
  "type": "object",
  "properties": {
    "summary": {"type": "string"},
    "tasks": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "title": {"type": "string"},
          "statement": {"type": "string"},
          "requirement": {"type": "string", "description": "A single requirement code, kept for compatibility. Prefer requirements."},
          "requirements": {"type": "array", "items": {"type": "string"}, "description": "One or more requirement codes this task implements."},
          "priority": {"enum": ["must", "should", "could"]},
          "estimate": {"type": "string"},
          "depends_on": {"type": "array", "items": {"type": "string"}}
        },
        "required": ["title", "statement", "priority"]
      }
    }
  },
  "required": ["summary", "tasks"],
  "additionalProperties": true
}`

// ImplementationBrief is the planner-stage output for one task (§50).
const ImplementationBrief = `{
  "type": "object",
  "properties": {
    "summary": {"type": "string"},
    "files": {"type": "array", "items": {"type": "string"}},
    "changes": {"type": "array", "items": {"type": "string"}},
    "target_requirement": {"type": "string"}
  },
  "required": ["summary"],
  "additionalProperties": true
}`

// Answers is the output for suggested answers to open questions.
const Answers = `{
  "type": "object",
  "properties": {
    "answers": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "question": {"type": "string"},
          "answer": {"type": "string"}
        },
        "required": ["question", "answer"]
      }
    }
  },
  "required": ["answers"],
  "additionalProperties": true
}`

// Convergence is the orchestrator-synthesis output (§M2 tail): a single
// synthesized recommendation reconciling a decision's dissenting positions.
const Convergence = `{
  "type": "object",
  "properties": {
    "summary": {"type": "string"}
  },
  "required": ["summary"]
}`
