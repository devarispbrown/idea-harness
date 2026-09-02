package web

// APIReferencePage returns the built api-reference.html — the standalone Scalar
// page Vite emits as its second entry point (web/api-reference.html).
//
// It is a separate document from the SPA on purpose: @scalar/api-reference is a
// Vue 3 application, so mounting it inside the React app would ship a second
// framework runtime to everyone who opens the UI. A separate entry point keeps
// it code-split, and the daemon serves it at /api-reference (see
// internal/daemon/apidocs.go) because the SPA is hash-routed and has no history
// fallback.
//
// ok is false on a checkout where the UI has not been built: dist/ is
// gitignored apart from the committed index.html placeholder, so
// api-reference.html simply is not there until `make build` runs. Callers are
// expected to degrade rather than fail — the spec itself is still served.
//
// Unlike index.html this page carries no token: it is a plain static document
// that fetches /openapi.yaml, which needs no credential.
func APIReferencePage() ([]byte, bool) {
	data, err := distFS.ReadFile("dist/api-reference.html")
	if err != nil {
		return nil, false
	}
	return data, true
}
