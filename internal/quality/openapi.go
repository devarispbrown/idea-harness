package quality

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// APIChange is one difference between the before and after versions of a
// public API surface -- an OpenAPI operation or a Go exported symbol.
//
// Subject is what a requirement or decision would name to acknowledge the
// change: the HTTP path for an OpenAPI change, the symbol name for a Go one.
// Path/Line cite where to look: the after file when the thing still exists
// there, otherwise the before file (a removal has no after line to point at).
type APIChange struct {
	Breaking bool   `json:"breaking"`
	Kind     string `json:"kind"`
	Subject  string `json:"subject"`
	Detail   string `json:"detail"`
	Path     string `json:"path"`
	Line     int    `json:"line,omitempty"`
}

// openAPIMethods are the operation keys of an OpenAPI 3 path item.
var openAPIMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// LooksLikeOpenAPI reports whether content is an OpenAPI 3 document: a
// top-level `openapi` key whose value starts with "3". Swagger 2 is not
// diffed (its shape differs and it is not what this repository's own tooling
// speaks), so a `swagger: "2.0"` file is not one.
func LooksLikeOpenAPI(content []byte) bool {
	var doc yaml.Node
	if yaml.Unmarshal(content, &doc) != nil || len(doc.Content) == 0 {
		return false
	}
	v, _ := mapGet(doc.Content[0], "openapi")
	return v != nil && v.Kind == yaml.ScalarNode && strings.HasPrefix(v.Value, "3")
}

// DiffOpenAPI compares two versions of an OpenAPI 3 document and reports
// what changed for a client. It is deliberately focused, not a full
// semantic diff: it answers "would an existing client of the before spec
// break against the after spec" for the cases that are unambiguous from the
// document alone.
//
// Breaking:
//   - a path or an operation removed;
//   - a parameter removed, a required parameter added, an optional parameter
//     made required, or a parameter's schema type changed;
//   - a request body made required, or a request body schema gaining a
//     required property or changing type;
//   - a 2xx response removed, a response media type removed, a response
//     schema's type changed, or a property removed from a response object.
//
// Not breaking (reported so a reader sees the whole surface change): a path,
// operation, optional parameter, or response property added.
//
// Out of scope, on purpose: enum narrowing/widening, format changes,
// constraint changes (minLength and the like), and anything behind
// oneOf/anyOf/allOf -- each needs a judgment about the client that the
// document does not carry. A schema it cannot type is skipped, never
// guessed at.
//
// beforePath/afterPath are the spec's workspace paths, used for citations.
func DiffOpenAPI(beforePath string, before []byte, afterPath string, after []byte) ([]APIChange, error) {
	b, err := parseYAMLDoc(before)
	if err != nil {
		return nil, fmt.Errorf("quality: parse %s (before): %w", beforePath, err)
	}
	a, err := parseYAMLDoc(after)
	if err != nil {
		return nil, fmt.Errorf("quality: parse %s (after): %w", afterPath, err)
	}
	d := openAPIDiff{before: b, after: a, beforePath: beforePath, afterPath: afterPath}
	d.run()
	sort.SliceStable(d.out, func(i, j int) bool {
		if d.out[i].Breaking != d.out[j].Breaking {
			return d.out[i].Breaking
		}
		return d.out[i].Subject < d.out[j].Subject
	})
	return d.out, nil
}

type openAPIDiff struct {
	before, after         *yaml.Node
	beforePath, afterPath string
	out                   []APIChange
}

func (d *openAPIDiff) add(breaking bool, kind, subject, detail string, inAfter bool, line int) {
	path := d.afterPath
	if !inAfter {
		path = d.beforePath
	}
	d.out = append(d.out, APIChange{Breaking: breaking, Kind: kind, Subject: subject, Detail: detail, Path: path, Line: line})
}

func (d *openAPIDiff) run() {
	bPaths, _ := mapGet(d.before, "paths")
	aPaths, aPathsKey := mapGet(d.after, "paths")
	for _, kv := range mapPairs(bPaths) {
		p := kv.key.Value
		aItem, _ := mapGet(aPaths, p)
		if aItem == nil {
			line := kv.key.Line
			inAfter := false
			if aPathsKey != nil {
				line, inAfter = aPathsKey.Line, true
			}
			d.add(true, "path_removed", p, "path "+p+" was removed", inAfter, line)
			continue
		}
		d.diffPathItem(p, d.resolve(d.before, kv.value), d.resolve(d.after, aItem))
	}
	for _, kv := range mapPairs(aPaths) {
		if v, _ := mapGet(bPaths, kv.key.Value); v == nil {
			d.add(false, "path_added", kv.key.Value, "path "+kv.key.Value+" was added", true, kv.key.Line)
		}
	}
}

func (d *openAPIDiff) diffPathItem(p string, b, a *yaml.Node) {
	bShared := d.params(d.before, b)
	aShared := d.params(d.after, a)
	for _, m := range openAPIMethods {
		bOp, _ := mapGet(b, m)
		aOp, aOpKey := mapGet(a, m)
		subject := strings.ToUpper(m) + " " + p
		switch {
		case bOp == nil && aOp == nil:
			continue
		case aOp == nil:
			d.add(true, "operation_removed", subject, "operation "+subject+" was removed", true, a.Line)
			continue
		case bOp == nil:
			d.add(false, "operation_added", subject, "operation "+subject+" was added", true, aOpKey.Line)
			continue
		}
		bOp, aOp = d.resolve(d.before, bOp), d.resolve(d.after, aOp)
		d.diffParams(subject, merge(bShared, d.params(d.before, bOp)), merge(aShared, d.params(d.after, aOp)), aOpKey.Line)
		d.diffRequestBody(subject, bOp, aOp, aOpKey.Line)
		d.diffResponses(subject, bOp, aOp, aOpKey.Line)
	}
}

// param is one resolved OpenAPI parameter.
type param struct {
	in, name string
	required bool
	typ      string
	line     int
}

func (d *openAPIDiff) params(doc, holder *yaml.Node) map[string]param {
	out := map[string]param{}
	list, _ := mapGet(holder, "parameters")
	if list == nil || list.Kind != yaml.SequenceNode {
		return out
	}
	for _, raw := range list.Content {
		n := d.resolve(doc, raw)
		name := scalar(n, "name")
		in := scalar(n, "in")
		if name == "" || in == "" {
			continue
		}
		schema, _ := mapGet(n, "schema")
		out[in+":"+name] = param{
			in: in, name: name,
			// A path parameter is required whatever the document says: the
			// spec mandates `required: true` for it, so treating a missing
			// flag as optional would invent a change.
			required: scalar(n, "required") == "true" || in == "path",
			typ:      d.schemaType(doc, schema, 0),
			line:     raw.Line,
		}
	}
	return out
}

func merge(shared, own map[string]param) map[string]param {
	out := make(map[string]param, len(shared)+len(own))
	for k, v := range shared {
		out[k] = v
	}
	for k, v := range own { // an operation's parameter overrides the path item's
		out[k] = v
	}
	return out
}

func (d *openAPIDiff) diffParams(subject string, b, a map[string]param, opLine int) {
	for _, k := range sortedKeys(b) {
		bp := b[k]
		ap, ok := a[k]
		label := bp.in + " parameter " + bp.name
		if !ok {
			d.add(true, "parameter_removed", subject, label+" was removed from "+subject, true, opLine)
			continue
		}
		if ap.required && !bp.required {
			d.add(true, "parameter_became_required", subject, label+" of "+subject+" is now required", true, ap.line)
		}
		if bp.typ != "" && ap.typ != "" && bp.typ != ap.typ {
			d.add(true, "parameter_type_changed", subject, fmt.Sprintf("%s of %s changed type: %s -> %s", label, subject, bp.typ, ap.typ), true, ap.line)
		}
	}
	for _, k := range sortedKeys(a) {
		if _, ok := b[k]; ok {
			continue
		}
		ap := a[k]
		label := ap.in + " parameter " + ap.name
		if ap.required {
			d.add(true, "required_parameter_added", subject, "required "+label+" was added to "+subject, true, ap.line)
		} else {
			d.add(false, "parameter_added", subject, "optional "+label+" was added to "+subject, true, ap.line)
		}
	}
}

func (d *openAPIDiff) diffRequestBody(subject string, bOp, aOp *yaml.Node, opLine int) {
	bBody, _ := mapGet(bOp, "requestBody")
	aBody, aKey := mapGet(aOp, "requestBody")
	if aBody == nil {
		return // dropping a body a client still sends is not something the document can call breaking
	}
	bBody, aBody = d.resolve(d.before, bBody), d.resolve(d.after, aBody)
	aReq := scalar(aBody, "required") == "true"
	bReq := bBody != nil && scalar(bBody, "required") == "true"
	if aReq && !bReq {
		d.add(true, "request_body_required", subject, "the request body of "+subject+" is now required", true, aKey.Line)
	}
	if bBody == nil {
		return
	}
	bContent, _ := mapGet(bBody, "content")
	aContent, _ := mapGet(aBody, "content")
	for _, kv := range mapPairs(bContent) {
		aMedia, aMediaKey := mapGet(aContent, kv.key.Value)
		if aMedia == nil {
			continue
		}
		bSchema, _ := mapGet(kv.value, "schema")
		aSchema, _ := mapGet(aMedia, "schema")
		bt, at := d.schemaType(d.before, bSchema, 0), d.schemaType(d.after, aSchema, 0)
		if bt != "" && at != "" && bt != at {
			d.add(true, "request_type_changed", subject, fmt.Sprintf("the %s request body of %s changed type: %s -> %s", kv.key.Value, subject, bt, at), true, aMediaKey.Line)
			continue
		}
		bRequired := d.requiredProps(d.before, bSchema)
		for _, name := range sortedSet(d.requiredProps(d.after, aSchema)) {
			if !bRequired[name] {
				d.add(true, "required_property_added", subject, fmt.Sprintf("the %s request body of %s now requires property %q", kv.key.Value, subject, name), true, aMediaKey.Line)
			}
		}
	}
}

func (d *openAPIDiff) diffResponses(subject string, bOp, aOp *yaml.Node, opLine int) {
	bResp, _ := mapGet(bOp, "responses")
	aResp, aRespKey := mapGet(aOp, "responses")
	for _, kv := range mapPairs(bResp) {
		code := kv.key.Value
		aR, aRKey := mapGet(aResp, code)
		if aR == nil {
			if strings.HasPrefix(code, "2") {
				line := opLine
				if aRespKey != nil {
					line = aRespKey.Line
				}
				d.add(true, "response_removed", subject, "the "+code+" response of "+subject+" was removed", true, line)
			}
			continue
		}
		bR, aR := d.resolve(d.before, kv.value), d.resolve(d.after, aR)
		bContent, _ := mapGet(bR, "content")
		aContent, _ := mapGet(aR, "content")
		for _, mkv := range mapPairs(bContent) {
			aMedia, aMediaKey := mapGet(aContent, mkv.key.Value)
			if aMedia == nil {
				d.add(true, "response_media_type_removed", subject, fmt.Sprintf("the %s response of %s no longer returns %s", code, subject, mkv.key.Value), true, aRKey.Line)
				continue
			}
			bSchema, _ := mapGet(mkv.value, "schema")
			aSchema, _ := mapGet(aMedia, "schema")
			bt, at := d.schemaType(d.before, bSchema, 0), d.schemaType(d.after, aSchema, 0)
			if bt != "" && at != "" && bt != at {
				d.add(true, "response_type_changed", subject, fmt.Sprintf("the %s %s response of %s changed type: %s -> %s", code, mkv.key.Value, subject, bt, at), true, aMediaKey.Line)
				continue
			}
			bProps := d.props(d.before, bSchema)
			aProps := d.props(d.after, aSchema)
			for _, name := range sortedSet(bProps) {
				if !aProps[name] {
					d.add(true, "response_property_removed", subject, fmt.Sprintf("the %s %s response of %s no longer has property %q", code, mkv.key.Value, subject, name), true, aMediaKey.Line)
				}
			}
			for _, name := range sortedSet(aProps) {
				if !bProps[name] {
					d.add(false, "response_property_added", subject, fmt.Sprintf("the %s %s response of %s gained property %q", code, mkv.key.Value, subject, name), true, aMediaKey.Line)
				}
			}
		}
	}
}

// schemaType renders a schema's type for comparison: "string", "object",
// "array<integer>", or "" when it cannot be told without guessing (a
// composite, an unresolvable $ref, no type at all on a non-object).
func (d *openAPIDiff) schemaType(doc, s *yaml.Node, depth int) string {
	s = d.resolve(doc, s)
	if s == nil || depth > 8 {
		return ""
	}
	t := scalar(s, "type")
	if t == "" {
		if v, _ := mapGet(s, "properties"); v != nil {
			return "object"
		}
		return ""
	}
	if t == "array" {
		items, _ := mapGet(s, "items")
		if it := d.schemaType(doc, items, depth+1); it != "" {
			return "array<" + it + ">"
		}
	}
	return t
}

// props returns the property names of an object schema, or of an array's
// item schema -- one level, since a response that is a list of objects loses
// a field the same way an object does.
func (d *openAPIDiff) props(doc, s *yaml.Node) map[string]bool {
	s = d.resolve(doc, s)
	if scalar(s, "type") == "array" {
		items, _ := mapGet(s, "items")
		s = d.resolve(doc, items)
	}
	out := map[string]bool{}
	ps, _ := mapGet(s, "properties")
	for _, kv := range mapPairs(ps) {
		out[kv.key.Value] = true
	}
	return out
}

func (d *openAPIDiff) requiredProps(doc, s *yaml.Node) map[string]bool {
	s = d.resolve(doc, s)
	out := map[string]bool{}
	req, _ := mapGet(s, "required")
	if req != nil && req.Kind == yaml.SequenceNode {
		for _, n := range req.Content {
			out[n.Value] = true
		}
	}
	return out
}

// resolve follows a local `$ref` (#/components/...) to its target, up to a
// small depth so a self-referential schema cannot loop. A remote ref, or one
// that does not resolve, returns the node unchanged -- callers then find no
// type on it and skip the comparison rather than inventing one.
func (d *openAPIDiff) resolve(doc, n *yaml.Node) *yaml.Node {
	for i := 0; n != nil && i < 8; i++ {
		ref := scalar(n, "$ref")
		if !strings.HasPrefix(ref, "#/") {
			return n
		}
		cur := doc
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			cur, _ = mapGet(cur, part)
			if cur == nil {
				return n
			}
		}
		n = cur
	}
	return n
}

// ---- yaml.Node helpers ----

func parseYAMLDoc(content []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return &yaml.Node{Kind: yaml.MappingNode}, nil
	}
	return doc.Content[0], nil
}

type pair struct{ key, value *yaml.Node }

func mapPairs(n *yaml.Node) []pair {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	out := make([]pair, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, pair{n.Content[i], n.Content[i+1]})
	}
	return out
}

// mapGet returns a mapping node's value for key, and the key node itself
// (whose Line is what a citation points at).
func mapGet(n *yaml.Node, key string) (value, keyNode *yaml.Node) {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1], n.Content[i]
		}
	}
	return nil, nil
}

func scalar(n *yaml.Node, key string) string {
	v, _ := mapGet(n, key)
	if v == nil || v.Kind != yaml.ScalarNode {
		return ""
	}
	return v.Value
}

func sortedKeys(m map[string]param) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
