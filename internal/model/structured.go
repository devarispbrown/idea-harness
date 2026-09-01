package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// StructuredResult is the outcome of the structured-output pipeline (§39).
type StructuredResult struct {
	Raw          []byte         // raw model output (post-redaction)
	Repaired     []byte         // repaired content (post-redaction)
	Valid        map[string]any // validated object
	WasRepaired  bool
}

// DecodeStructured validates raw JSON against schema, applying bounded repair on
// failure (single pass). It returns the validated object and whether repair was
// used. Raw and repaired content are returned for digest storage.
func DecodeStructured(raw []byte, schema []byte) (*StructuredResult, error) {
	if len(schema) == 0 {
		return nil, fmt.Errorf("model: structured output requires a schema")
	}
	compiled, err := compileSchema(schema)
	if err != nil {
		return nil, err
	}

	// Pre-extract the JSON object, stripping markdown fences and surrounding
	// prose. Real models frequently wrap output in ```json … ```.
	extracted := extractJSON(raw)

	// Fast path: valid as-is.
	if obj, ok := validateRaw(compiled, extracted); ok {
		return &StructuredResult{Raw: raw, Repaired: extracted, Valid: obj}, nil
	}

	repaired := repairJSON(extracted)
	if obj, ok := validateRaw(compiled, repaired); ok {
		return &StructuredResult{Raw: raw, Repaired: repaired, Valid: obj, WasRepaired: true}, nil
	}

	if strings.TrimSpace(string(extracted)) == "" {
		return nil, fmt.Errorf("model: structured output failed: model returned empty output")
	}
	// Do NOT embed the raw model output in the error: it is untrusted and may
	// contain credentials that would then leak into logs, failure reasons, and
	// the event stream (review: v1 security boundary).
	return nil, fmt.Errorf("model: structured output failed schema validation after bounded repair")
}

func compileSchema(schema []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return nil, fmt.Errorf("model: parse schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	const url = "https://idea-harness.local/schemas/output"
	if err := c.AddResource(url, doc); err != nil {
		return nil, fmt.Errorf("model: add schema: %w", err)
	}
	return c.Compile(url)
}

func validateRaw(sch *jsonschema.Schema, raw []byte) (map[string]any, bool) {
	trimmed := bytes.TrimSpace(raw)
	var obj any
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return nil, false
	}
	if err := sch.Validate(obj); err != nil {
		return nil, false
	}
	m, ok := obj.(map[string]any)
	if !ok {
		return nil, false
	}
	return m, true
}

// extractJSON strips markdown fences and surrounding prose, returning the
// outermost JSON object (or the raw text unchanged if no object is found). It
// tracks string literals so braces/brackets inside answer text are not mistaken
// for the object boundary.
func extractJSON(raw []byte) []byte {
	s := strings.TrimSpace(string(raw))
	// Strip ```json / ``` fences.
	if strings.HasPrefix(s, "```") {
		if nl := strings.Index(s, "\n"); nl >= 0 {
			s = s[nl+1:]
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
	}
	start := strings.Index(s, "{")
	if start == -1 {
		return []byte(strings.TrimSpace(s))
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return []byte(strings.TrimSpace(s[start : i+1]))
			}
		}
	}
	// Unbalanced — return from the opening brace to the end.
	return []byte(strings.TrimSpace(s[start:]))
}

// closers returns the closing characters needed to balance unclosed containers,
// ignoring any braces/brackets inside string literals.
func closers(s string) []byte {
	var stack []byte
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}':
			if n := len(stack); n > 0 && stack[n-1] == '}' {
				stack = stack[:n-1]
			}
		case ']':
			if n := len(stack); n > 0 && stack[n-1] == ']' {
				stack = stack[:n-1]
			}
		}
	}
	return stack
}

// repairJSON applies a bounded, deterministic JSON repair: extract the outer
// object, strip trailing commas, and close unterminated strings/containers.
// It is deliberately conservative — it never invents field values.
func repairJSON(raw []byte) []byte {
	s := strings.TrimSpace(string(extractJSON(raw)))

	// Remove trailing commas before a closing bracket/brace.
	s = strings.ReplaceAll(s, ",}", "}")
	s = strings.ReplaceAll(s, ",]", "]")

	// Close an unterminated final string value.
	if strings.Count(s, `"`)%2 == 1 {
		s += `"`
	}

	// Close unclosed containers in correct (LIFO) nesting order, skipping any
	// braces inside string literals.
	stack := closers(s)
	for i := len(stack) - 1; i >= 0; i-- {
		s += string(stack[i])
	}

	return []byte(s)
}
