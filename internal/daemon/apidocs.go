package daemon

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"strconv"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/devarispbrown/idea-harness/docs"
	"github.com/devarispbrown/idea-harness/internal/web"
)

// The API description and the page that renders it live *outside* /api/, and
// that is load-bearing rather than cosmetic.
//
// Server.auth guards every path under /api/ with the per-install token, exempt
// only /api/v1/health and the SSE token= query parameter. A Scalar page fetching
// /api/v1/openapi.yaml would therefore get a silent 401 and render blank: it is
// a plain fetch() with no token, and there is nowhere to put one on a page that
// deliberately carries no credential.
//
// Serving them at the root instead needs no change to the auth boundary, and
// costs nothing in exposure: the spec is a description of the route table with
// no secrets in it, served by a daemon that binds loopback only and refuses
// non-loopback Host/Origin — and "/" already serves index.html with the API
// token injected into it, so the spec is strictly the less sensitive of the two.

// apiReferenceCSP locks the reference page to its own origin.
//
// It is deliberately its own policy rather than the UI's: this document carries
// no token, but it does bundle a third-party Vue application, so the useful
// property here is that nothing it runs can reach off-origin — connect-src
// 'self' also means Scalar's "Test Request" proxy (proxy.scalar.com) is
// unreachable, which is why the client is disabled in the page's config rather
// than left to fail silently at runtime.
//
// It is as strict as the UI's, and verified against the built bundle rather
// than guessed at: the emitted chunks contain no `new Function`, no `eval(` and
// no `new Worker`, so neither 'unsafe-eval' nor a worker-src exemption is
// needed. style-src allows inline styles because Scalar sets them as attributes;
// img-src allows blob: because downloads go through createObjectURL; font-src is
// 'self' plus data: and the page is configured withDefaultFonts:false, so it
// never reaches for fonts.scalar.com.
const apiReferenceCSP = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'; " +
	"form-action 'none'"

// registerAPIDocs mounts the self-description endpoints on mux:
//
//	GET /openapi.yaml    the embedded spec, verbatim
//	GET /openapi.json    the same document converted to JSON
//	GET /api-reference   a standalone Scalar page rendering it
//
// Call it from Server.routes. These patterns are more specific than the "/"
// pattern the embedded UI is mounted on, so ServeMux prefers them regardless of
// registration order.
//
// The patterns are written as plain string literals, not assembled from
// constants, because `make api-check` reads them out of the AST and rejects any
// HandleFunc in this package whose pattern it cannot read — a route that the
// checker cannot see is a route that can drift out of the spec unnoticed.
func registerAPIDocs(mux *http.ServeMux) {
	mux.HandleFunc("GET /openapi.yaml", handleOpenAPIYAML)
	mux.HandleFunc("GET /openapi.json", handleOpenAPIJSON)
	mux.HandleFunc("GET /api-reference", handleAPIReference)
}

func handleOpenAPIYAML(w http.ResponseWriter, _ *http.Request) {
	// RFC 9512 registers application/yaml; charset is required to keep browsers
	// from guessing an encoding for a UTF-8 document.
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	// The spec changes with the binary and has no versioned URL, so a cached
	// copy would outlive an upgrade. no-cache still allows revalidation.
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(docs.OpenAPISpec)
}

func handleOpenAPIJSON(w http.ResponseWriter, _ *http.Request) {
	data, err := openAPIJSON()
	if err != nil {
		// A malformed spec is a build-time mistake, not a client error: the
		// YAML endpoint still works, so say which one is broken.
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("openapi.yaml is not valid YAML: %v", err))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func handleAPIReference(w http.ResponseWriter, _ *http.Request) {
	page, ok := web.APIReferencePage()
	if !ok {
		// 404, deliberately: this document is genuinely not in this binary, and
		// answering 200 for a page that does not exist is the worse lie. The
		// body explains which of the two ways that happened and points at the
		// spec, which is embedded and therefore always available.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Security-Policy", apiReferenceCSP)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write(apiReferenceUnbuiltPage(web.State(), os.Getenv(apiReferenceURLEnv)))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Vite hashes the asset filenames this document points at, so a stale copy
	// would reference assets a newer binary no longer embeds.
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", apiReferenceCSP)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(page)
}

// apiReferenceURLEnv names a reference hosted somewhere else — set it on a
// binary built without the bundled renderer, and the fallback page links there.
// An environment variable rather than project config, like IDEA_HARNESS_STATE
// and IDEA_HARNESS_ADDR: which documentation this daemon points at is a property
// of the install, not of the product being built.
const apiReferenceURLEnv = "IDEA_HARNESS_API_REFERENCE_URL"

// apiReferenceUnbuiltPage explains an absent reference in terms of how this
// binary was actually built.
//
// The distinction is the point. A checkout that never ran `make build` should
// run it; a binary built with APIDOCS=0 already did, and telling its operator
// to "run make build" would be false — which is exactly what this page used to
// say, because it had only a boolean to go on.
func apiReferenceUnbuiltPage(state web.BuildState, hostedURL string) []byte {
	var explanation, remedy string
	switch state {
	case web.BuildWithoutReference:
		explanation = "This binary was built without the bundled API reference " +
			"(<code>APIDOCS=0</code>), which keeps about 3.7 MiB out of it."
		remedy = "Build with <code>make build</code> to embed the reference, or host it " +
			"separately — see <code>make api-docs-dist</code>."
	default:
		explanation = "The web UI has not been built in this checkout, so neither the UI " +
			"nor the API reference is embedded."
		remedy = "Run <code>make build</code> to build and embed them."
	}

	var hosted string
	if hostedURL != "" {
		// Escaped, not interpolated: this is an environment value landing in
		// an HTML attribute.
		hosted = "<p>This install points at a hosted reference: <a href=\"" +
			html.EscapeString(hostedURL) + "\">" + html.EscapeString(hostedURL) + "</a></p>"
	}

	return []byte(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>API reference not built</title></head>
<body>
<h1>API reference not built</h1>
<p>` + explanation + `</p>
` + hosted + `
<p>The API description itself is always embedded:</p>
<ul>
  <li><a href="/openapi.yaml">/openapi.yaml</a></li>
  <li><a href="/openapi.json">/openapi.json</a></li>
</ul>
<p>` + remedy + `</p>
</body></html>
`)
}

// openAPIJSON converts the embedded YAML spec to JSON once and caches the
// result. OpenAPI is defined over the JSON data model, so this is a
// representation change rather than a translation: tooling that cannot read
// YAML gets the same document.
var openAPIJSON = sync.OnceValues(func() ([]byte, error) {
	var doc any
	if err := yaml.Unmarshal(docs.OpenAPISpec, &doc); err != nil {
		return nil, err
	}
	converted, err := jsonValue(doc)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(converted, "", "  ")
})

// jsonValue rewrites a YAML-decoded value into something encoding/json accepts.
//
// yaml.v3 decodes a mapping into map[string]any only when every key is a
// string; an OpenAPI document has plenty of keys that are not (`200:` is an
// integer, `on:` in an example could be a bool), and those come back as
// map[any]any, which json.Marshal rejects outright. JSON object keys are always
// strings, so stringifying them is the correct conversion, not a workaround.
func jsonValue(v any) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			conv, err := jsonValue(val)
			if err != nil {
				return nil, err
			}
			out[k] = conv
		}
		return out, nil
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			key, err := jsonKey(k)
			if err != nil {
				return nil, err
			}
			conv, err := jsonValue(val)
			if err != nil {
				return nil, err
			}
			out[key] = conv
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			conv, err := jsonValue(val)
			if err != nil {
				return nil, err
			}
			out[i] = conv
		}
		return out, nil
	default:
		return v, nil
	}
}

func jsonKey(k any) (string, error) {
	switch t := k.(type) {
	case string:
		return t, nil
	case int:
		return strconv.Itoa(t), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64), nil
	case bool:
		return strconv.FormatBool(t), nil
	case nil:
		return "null", nil
	default:
		return "", fmt.Errorf("mapping key %v (%T) cannot be a JSON object key", k, k)
	}
}
