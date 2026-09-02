package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// writeSpecJSON converts docs/openapi.yaml to JSON at dest.
//
// The daemon does this conversion at request time (internal/daemon/apidocs.go)
// so `/openapi.json` works without a second checked-in file. A static bundle has
// no daemon to ask, and duplicating the conversion in a shell pipeline would
// mean a second implementation of "what OpenAPI JSON looks like" — so the
// generator that already parses this file writes it.
//
// OpenAPI is defined over the JSON data model, so this is a change of
// representation rather than a translation: the same document, for tooling that
// cannot read YAML.
func writeSpecJSON(dest string) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	specPath := filepath.Join(root, "docs", "openapi.yaml")
	raw, err := os.ReadFile(specPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", specPath, err)
	}

	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", specPath, err)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}
	out = append(out, '\n')

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dest, out, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d bytes)\n", dest, len(out))
	return nil
}
