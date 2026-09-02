// Package docs embeds the OpenAPI description of the daemon API so a compiled
// binary can describe itself with no network and no checkout.
//
// The Go file lives in docs/ rather than in internal/ because //go:embed
// patterns cannot contain ".." — the file has to sit beside the spec. Keeping
// the spec at docs/openapi.yaml (rather than copying it under internal/) means
// there is exactly one copy: the file a human edits, the file `make api-check`
// verifies against internal/daemon/routes.go, and the bytes the daemon serves
// at /openapi.yaml are the same bytes.
package docs

import _ "embed"

// OpenAPISpec is docs/openapi.yaml verbatim. It is served, unmodified, by the
// daemon's GET /openapi.yaml; GET /openapi.json converts it on demand.
//
// Treat it as read-only: it is backed by the binary's read-only data section
// in spirit, and every caller shares the slice.
//
//go:embed openapi.yaml
var OpenAPISpec []byte
