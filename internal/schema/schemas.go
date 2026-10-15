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
          "code": {"type": "string", "description": "When revising, the EXACT existing code (e.g. \"REQ-4\") of the requirement this one continues/updates, copied from the Requirements context. Leave empty for a genuinely new requirement."},
          "decision": {"type": "string", "description": "Exact question text of the decision this requirement is shaped by, if any."},
          "objectives": {"type": "array", "items": {"type": "string"}, "description": "The key(s) or OBJ code(s) of the objective(s) listed under objectives that this requirement serves. Every requirement must serve at least one."},
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
          "recommendation": {
            "type": "string",
            "description": "When options is non-empty, this MUST be either (a) one entry from options, copied verbatim character-for-character -- no paraphrasing, no added punctuation or wording -- or (b) the 1-based index of that entry as a string (e.g. \"2\"). A recommendation that is neither cannot be auto-accepted and will be left for a human to resolve by hand."
          }
        },
        "required": ["question", "options", "recommendation"]
      }
    },
    "objectives": {
      "type": "array",
      "description": "The product outcomes this initiative exists to move -- what success looks like for users or the business, not features. Every requirement must serve at least one objective, and every objective needs at least one measurable metric.",
      "items": {
        "type": "object",
        "properties": {
          "key": {"type": "string", "description": "When revising, the EXACT existing code (e.g. \"OBJ-1\") of the objective this one continues, copied from the Objectives context. Otherwise a short new key (e.g. \"O1\") that requirements cite in their objectives field."},
          "statement": {"type": "string", "description": "The outcome, stated as a change in the world (e.g. \"New users reach their first successful sync on day one\")."},
          "rationale": {"type": "string", "description": "Why this outcome matters."},
          "metrics": {
            "type": "array",
            "items": {
              "type": "object",
              "properties": {
                "key": {"type": "string", "description": "When revising, the EXACT existing code (e.g. \"MET-2\") of the metric this one continues; otherwise leave empty."},
                "name": {"type": "string"},
                "definition": {"type": "string", "description": "Exactly how the number is computed."},
                "unit": {"type": "string"},
                "direction": {"enum": ["increase", "decrease", "maintain"]},
                "baseline": {"type": ["number", "null"], "description": "The value today, if known. Omit or null when unknown -- do not invent one."},
                "target": {"type": "number"},
                "guardrail": {"type": ["number", "null"], "description": "Optional hard limit on the bad side: a floor for increase, a ceiling for decrease, the largest allowed distance from the target for maintain."},
                "cadence": {"type": "string", "description": "How often it should be observed: hourly, daily, weekly, biweekly, monthly, quarterly, <n>d, <n>w, or a duration like 36h."},
                "hypothesis": {"type": "string", "description": "We believe <which requirement(s) / change> will move this metric <how>. Recorded as a hypothesis linked to the metric."}
              },
              "required": ["name", "direction", "target"]
            }
          }
        },
        "required": ["statement", "metrics"]
      }
    },
    "open_questions": {"type": "array", "items": {"type": "string"}},
    "risks": {"type": "array", "items": {"type": "string"}},
    "review_responses": {
      "type": "array",
      "description": "When revising after a review, one entry per finding listed under review_findings: what this revision did about it.",
      "items": {
        "type": "object",
        "properties": {
          "finding": {"type": "string", "description": "The finding's label exactly as listed, e.g. \"F2\"."},
          "response": {"type": "string", "description": "What changed, naming the requirement code(s) -- or why nothing needed to."}
        },
        "required": ["finding", "response"]
      }
    }
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
          "in_diff": {"type": "boolean", "description": "Self-report: true if this finding is about code the change set actually touches. Only consulted when path is omitted (review B2)."},
          "requirement": {"type": "string", "description": "The code of the requirement (or task, objective or metric) this finding is about, exactly as listed -- or \"idea\" for something the idea asks for that no requirement covers, with evidence quoting the idea. A critical/high finding citing none of these is recorded as advisory."},
          "reraises": {"type": "string", "description": "When this repeats a finding listed under prior_findings, that finding's label, e.g. \"F1\"."},
          "justification": {"type": "string", "description": "Required with reraises when the prior finding was addressed: specifically why the revision did not resolve it."}
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
          "recommendation": {
            "type": "string",
            "description": "When options is non-empty, this MUST be either (a) one entry from options, copied verbatim character-for-character -- no paraphrasing, no added punctuation or wording -- or (b) the 1-based index of that entry as a string (e.g. \"2\"). A recommendation that is neither cannot be auto-accepted and will be left for a human to resolve by hand."
          },
          "requirements": {
            "type": "array",
            "items": {"type": "string"},
            "description": "The Requirement code(s) (e.g. \"REQ-2\") this decision shapes, if any -- so resolving or reopening it can flag the requirements, tasks and change sets built on it as possibly invalid. Leave empty only when the decision is not about a specific requirement."
          }
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
          "recommendation": {
            "type": "string",
            "description": "When options is non-empty, this MUST be either (a) one entry from options, copied verbatim character-for-character -- no paraphrasing, no added punctuation or wording -- or (b) the 1-based index of that entry as a string (e.g. \"2\"). A recommendation that is neither cannot be auto-accepted and will be left for a human to resolve by hand."
          },
          "requirements": {
            "type": "array",
            "items": {"type": "string"},
            "description": "The Requirement code(s) (e.g. \"REQ-2\") this decision shapes, if any -- so resolving or reopening it can flag the requirements, tasks and change sets built on it as possibly invalid. Leave empty only when the decision is not about a specific requirement."
          }
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

// QualityReview is the verification_engineer's model-reviewed quality check
// (verify's api_ergonomics dimension, and docs_completeness/
// operational_readiness when verify.model_review names them). Every finding
// must cite a path and a line inside the diff it was shown; one that does not
// is recorded but never affects the scorecard. unreviewed_reason is how a
// provider that cannot actually review (the demo provider) says so, which
// the dimension then reports as not_measured rather than a clean pass.
const QualityReview = `{
  "type": "object",
  "properties": {
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "severity": {"enum": ["critical", "high", "medium", "low", "observation"]},
          "category": {"type": "string"},
          "finding": {"type": "string"},
          "path": {"type": "string", "description": "The file the finding is about, exactly as shown in the diff header."},
          "line": {"type": "integer", "description": "The line number in the NEW version of the file, inside one of the diff's hunks."},
          "recommendation": {"type": "string"},
          "requirement": {"type": "string", "description": "The requirement code this finding bears on, if any."}
        },
        "required": ["severity", "category", "finding", "path", "line"]
      }
    },
    "unreviewed_reason": {"type": "string", "description": "Set only when you could not review the diff at all, saying why."}
  },
  "required": ["findings"],
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
