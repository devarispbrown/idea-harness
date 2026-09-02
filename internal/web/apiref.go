package web

import "bytes"

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
// ok is false in two different situations, which callers must be able to tell
// apart — see BuildState.
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

// BuildState describes which parts of the web UI this binary was built with.
type BuildState int

const (
	// BuildComplete: both the SPA and the API reference are embedded.
	BuildComplete BuildState = iota
	// BuildWithoutReference: the UI was built, but without the API reference
	// entry — an `APIDOCS=0 make build`, which trades the reference's ~3.7 MiB
	// for a smaller binary and expects the reference to be hosted elsewhere.
	BuildWithoutReference
	// BuildWithoutUI: the UI was never built. dist/ is gitignored apart from a
	// committed index.html placeholder, so this is what a fresh checkout that
	// has only run `go build` looks like.
	BuildWithoutUI
)

// State reports how this binary was built.
//
// The distinction matters because the two absences need opposite advice: a
// checkout that never ran `make build` should run it, while an APIDOCS=0 build
// already did — telling its operator to "run make build" would be false, which
// is what the single boolean this replaces led the fallback page to say.
//
// It is inferred from what is embedded rather than stamped at build time: the
// SPA's own index.html is the marker. A real build overwrites the committed
// placeholder with a document that loads a hashed asset bundle; the placeholder
// does not.
func State() BuildState {
	index, err := distFS.ReadFile("dist/index.html")
	if err != nil {
		return BuildWithoutUI
	}
	if !containsBuiltAssets(index) {
		return BuildWithoutUI
	}
	if _, ok := APIReferencePage(); !ok {
		return BuildWithoutReference
	}
	return BuildComplete
}

// containsBuiltAssets reports whether an index.html is Vite output rather than
// the committed placeholder. Vite rewrites the entry to a hashed bundle under
// /assets/; the placeholder has no script at all.
func containsBuiltAssets(index []byte) bool {
	return bytes.Contains(index, []byte("/assets/"))
}
