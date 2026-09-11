package main

// Structural validity of the schemas in the spec.
//
// `make api-check` proves every route has an operation and every body field is
// documented. It does not read schemas as JSON Schema, and one of them was not
// valid JSON Schema at all: `required: [status]` was indented one level too far
// in updateExecutors' 200, landing inside `properties:`. Parsed, that declares
// a property NAMED "required" whose schema is the sequence ["status"] -- not a
// schema object or a boolean, so it is invalid -- and silently loses the actual
// required constraint.
//
// A generated client reads that node. Nothing in the three api-check axes, the
// route walker, or the response-schema test looks at it: the last decodes only
// `Get` operations and only the "200" body.

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// checkSchemaShapes walks every `properties` node in the spec and reports any
// whose value is not a schema object or boolean.
func checkSchemaShapes(doc any) []string {
	var problems []string
	var walk func(n any, path string)
	walk = func(n any, path string) {
		switch v := n.(type) {
		case map[string]any:
			if props, ok := v["properties"].(map[string]any); ok {
				names := make([]string, 0, len(props))
				for name := range props {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					switch props[name].(type) {
					case map[string]any, bool:
					default:
						problems = append(problems, fmt.Sprintf(
							"  %s/properties/%s is %T, not a schema.\n"+
								"    A JSON Schema property must be an object or a boolean. This is what an\n"+
								"    over-indented `required:` looks like once parsed: a property named\n"+
								"    \"required\" whose value is a list, and the real constraint gone.",
							path, name, props[name]))
					}
				}
			}
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(v[k], path+"/"+k)
			}
		case []any:
			for i, item := range v {
				walk(item, fmt.Sprintf("%s/%d", path, i))
			}
		}
	}
	walk(doc, "")
	return problems
}

// schemaShapeError formats the problems, or nil when there are none.
func schemaShapeError(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("docs/openapi.yaml has schemas a client cannot read:\n%s",
		strings.Join(problems, "\n"))
}

// checkDeclaredStatuses reports operations that can answer a status they do not
// declare.
//
// Two rules, both mechanical:
//
//   - every non-GET operation can answer 500, because writeErr promotes any
//     store.IsStorageFailure to 500 and every mutating handler touches the
//     store. Ten of thirty-six did not declare it.
//   - every operation with a path parameter can answer 404, because the id in
//     that parameter has to be resolved before anything else happens.
//
// The drift is one-directional in practice — no operation declared a code its
// handler cannot emit — so this checks only for under-declaration, which is the
// half a generated client is hurt by.
//
// Deliberately not satisfied by a `default:` response. A client generator turns
// `default` into a catch-all error branch; it cannot tell the caller that THIS
// operation answers 404 for a bad id and 500 for a broken store.
func checkDeclaredStatuses(doc any) []string {
	root, ok := doc.(map[string]any)
	if !ok {
		return []string{"  spec is not a mapping"}
	}
	paths, ok := root["paths"].(map[string]any)
	if !ok {
		return []string{"  spec has no paths mapping"}
	}
	var problems []string
	routes := make([]string, 0, len(paths))
	for p := range paths {
		routes = append(routes, p)
	}
	sort.Strings(routes)

	for _, route := range routes {
		methods, ok := paths[route].(map[string]any)
		if !ok {
			continue
		}
		verbs := make([]string, 0, len(methods))
		for m := range methods {
			verbs = append(verbs, m)
		}
		sort.Strings(verbs)
		for _, verb := range verbs {
			op, ok := methods[verb].(map[string]any)
			if !ok {
				continue
			}
			responses, ok := op["responses"].(map[string]any)
			if !ok {
				continue
			}
			id, _ := op["operationId"].(string)
			// 403 (host/origin guard) and 405 (wrong method) are emitted at the
			// routing layer for every path under /api/ and declared on none, by
			// a deliberate choice the spec argues for. `default` is what makes
			// that choice survivable for a generated client: without it an
			// undeclared status is an unhandled branch.
			if _, has := responses["default"]; !has {
				problems = append(problems, fmt.Sprintf(
					"  %s has no default response; 403 and 405 are emitted on every operation\n"+
						"    and declared on none, so a client has nowhere to put them", id))
			}
			if verb != "get" {
				if _, has := responses["500"]; !has {
					problems = append(problems, fmt.Sprintf(
						"  %s does not declare 500, but writeErr promotes any storage failure to it", id))
				}
			}
			if strings.Contains(route, "{") {
				if _, has := responses["404"]; !has {
					problems = append(problems, fmt.Sprintf(
						"  %s takes a path id and does not declare 404", id))
				}
			}
			// Every project-scoped operation loads harness.yaml while resolving
			// the project, so a malformed file is reachable from all of them.
			if strings.Contains(route, "{projectId}") {
				if _, has := responses["422"]; !has {
					problems = append(problems, fmt.Sprintf(
						"  %s is project-scoped and does not declare 422; a malformed harness.yaml\n"+
							"    is reachable from every operation that resolves a project", id))
				}
			}
		}
	}
	return problems
}

// declaredStatusError formats the problems, or nil when there are none.
func declaredStatusError(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("docs/openapi.yaml under-declares statuses its handlers emit:\n%s",
		strings.Join(problems, "\n"))
}

// operationCountRe matches the spec's own claim about how many operations it
// has. The claim is load-bearing: it is the justification for not declaring 404
// and 405 per operation, and a justification that miscounts its own subject is
// the weakest possible form of one.
//
// It said 84 against an actual 81 — written when the API had 84 operations and
// never revisited as routes were removed. Nothing checked it because it is
// prose.
var operationCountRe = regexp.MustCompile(`repeating them (\d+) times`)

// checkOperationCountClaim compares the spec's stated operation count to the
// number of operations it actually defines.
func checkOperationCountClaim(raw string, doc any) []string {
	m := operationCountRe.FindStringSubmatch(raw)
	if m == nil {
		return []string{"  the spec no longer states an operation count in the shape this check reads;\n" +
			"    if the sentence moved, move this check with it rather than deleting it"}
	}
	claimed, err := strconv.Atoi(m[1])
	if err != nil {
		return []string{fmt.Sprintf("  operation count claim %q is not a number", m[1])}
	}
	actual := 0
	if root, ok := doc.(map[string]any); ok {
		if paths, ok := root["paths"].(map[string]any); ok {
			for _, methods := range paths {
				mm, ok := methods.(map[string]any)
				if !ok {
					continue
				}
				for _, op := range mm {
					if o, ok := op.(map[string]any); ok {
						if _, has := o["responses"]; has {
							actual++
						}
					}
				}
			}
		}
	}
	if claimed != actual {
		return []string{fmt.Sprintf(
			"  the spec says it has %d operations; it has %d.\n"+
				"    That number is the stated reason 404 and 405 are not declared per operation,\n"+
				"    so it is an argument about a set whose size it gets wrong.", claimed, actual)}
	}
	return nil
}

// checkRefsResolve reports a `$ref` that points nowhere.
//
// A dangling ref is worse than a missing declaration: most generators fail to
// build on it, and nothing in this repo looked. checkDeclaredStatuses is
// presence-only, so `"422": { $ref: "#/components/responses/DoesNotExist" }`
// satisfied it — as did pointing 422 at NotFound, which resolves and is simply
// the wrong response. This catches the first; the second is what reading the
// diff is for.
func checkRefsResolve(doc any) []string {
	root, ok := doc.(map[string]any)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	var refs []string
	var collect func(n any)
	collect = func(n any) {
		switch v := n.(type) {
		case map[string]any:
			if r, ok := v["$ref"].(string); ok && !seen[r] {
				seen[r] = true
				refs = append(refs, r)
			}
			for _, val := range v {
				collect(val)
			}
		case []any:
			for _, item := range v {
				collect(item)
			}
		}
	}
	collect(root)
	sort.Strings(refs)

	var problems []string
	for _, ref := range refs {
		if !strings.HasPrefix(ref, "#/") {
			problems = append(problems, fmt.Sprintf(
				"  $ref %q is not a local document reference; nothing here can resolve it", ref))
			continue
		}
		node := any(root)
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			m, ok := node.(map[string]any)
			if !ok {
				node = nil
				break
			}
			node, ok = m[part]
			if !ok {
				node = nil
				break
			}
		}
		if node == nil {
			problems = append(problems, fmt.Sprintf(
				"  $ref %q resolves to nothing; a generated client fails to build on this", ref))
		}
	}
	return problems
}
