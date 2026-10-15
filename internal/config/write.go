package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// WriteHarness writes harness.yaml to dir, applying defaults first. It is the
// single write path for harness configuration -- `idea model set`, `idea
// executors setup` and every Settings and Automations save go through it (§47:
// no secrets written -- only *_env references are permitted).
//
// The candidate is held to exactly what Load will hold it to -- the JSON
// Schema, the inline-secret sweep, and the structural checks -- before a byte
// is written. It used to check only `network`, so a writer could save a file
// the next Load refused, and every project-scoped route then answered with
// that file's parse error until someone hand-edited it back.
//
// Comments a person wrote into an existing harness.yaml are kept: the file is
// committable and hand-edited, and a Settings save that silently dropped
// every `# why we pinned this image` note was a reason not to use Settings.
// Keys keep their existing order; new keys are appended where the struct
// puts them.
func WriteHarness(dir string, h HarnessConfig) error {
	data, err := MarshalHarness(h)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "harness.yaml")
	if existing, err := os.ReadFile(path); err == nil {
		if merged, err := preserveComments(existing, data); err == nil {
			data = merged
		}
		// A file that does not parse has no comments worth keeping that the
		// next Load would not already refuse; the fresh document replaces it.
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", dir, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}

// MarshalHarness applies defaults, validates h exactly as Load would, and
// returns the YAML WriteHarness would write (without comment preservation).
func MarshalHarness(h HarnessConfig) ([]byte, error) {
	cfg := Config{Harness: h}
	cfg.applyDefaults()
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg.Harness); err != nil {
		return nil, fmt.Errorf("config: marshal harness: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("config: marshal harness: %w", err)
	}
	data := buf.Bytes()
	if err := ValidateHarnessYAML(data); err != nil {
		return nil, err
	}
	return data, nil
}

// ValidateHarnessYAML runs Load's harness.yaml checks on a document.
func ValidateHarnessYAML(data []byte) error {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return &ParseError{File: "harness.yaml", Msg: err.Error()}
	}
	if doc == nil {
		doc = map[string]any{}
	}
	if err := validateAgainst("harness.yaml", harnessSchema, doc); err != nil {
		return err
	}
	if err := rejectSecrets("", doc); err != nil {
		return err
	}
	cfg := Default()
	if err := yaml.Unmarshal(data, &cfg.Harness); err != nil {
		return fmt.Errorf("config: parse harness.yaml: %w", err)
	}
	cfg.applyDefaults()
	return cfg.Harness.validateStruct()
}

// preserveComments returns fresh (the newly marshalled document) carrying
// over the comments and key order of old. Only comments are taken from old:
// every value comes from fresh, so a key the write removed stays removed.
func preserveComments(old, fresh []byte) ([]byte, error) {
	var oldDoc, newDoc yaml.Node
	if err := yaml.Unmarshal(old, &oldDoc); err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(fresh, &newDoc); err != nil {
		return nil, err
	}
	if len(oldDoc.Content) == 0 || len(newDoc.Content) == 0 {
		return fresh, nil
	}
	newDoc.HeadComment, newDoc.FootComment = oldDoc.HeadComment, oldDoc.FootComment
	mergeNode(oldDoc.Content[0], newDoc.Content[0])
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&newDoc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// mergeNode copies old's comments onto fresh, recursively, and reorders a
// mapping's keys to old's order (keys only fresh has keep their relative
// order, after the ones old already had).
func mergeNode(old, fresh *yaml.Node) {
	fresh.HeadComment = old.HeadComment
	fresh.LineComment = old.LineComment
	fresh.FootComment = old.FootComment
	if old.Kind != fresh.Kind {
		return
	}
	switch fresh.Kind {
	case yaml.MappingNode:
		oldIdx := map[string]int{}
		for i := 0; i+1 < len(old.Content); i += 2 {
			oldIdx[old.Content[i].Value] = i
		}
		type pair struct{ k, v *yaml.Node }
		var kept, added []pair
		keptPos := map[string]int{}
		for i := 0; i+1 < len(fresh.Content); i += 2 {
			k, v := fresh.Content[i], fresh.Content[i+1]
			if j, ok := oldIdx[k.Value]; ok {
				k.HeadComment, k.LineComment, k.FootComment = old.Content[j].HeadComment, old.Content[j].LineComment, old.Content[j].FootComment
				mergeNode(old.Content[j+1], v)
				keptPos[k.Value] = j
				kept = append(kept, pair{k, v})
			} else {
				added = append(added, pair{k, v})
			}
		}
		// Stable sort of kept pairs by their position in old.
		for i := 1; i < len(kept); i++ {
			for j := i; j > 0 && keptPos[kept[j-1].k.Value] > keptPos[kept[j].k.Value]; j-- {
				kept[j-1], kept[j] = kept[j], kept[j-1]
			}
		}
		fresh.Content = fresh.Content[:0]
		for _, p := range append(kept, added...) {
			fresh.Content = append(fresh.Content, p.k, p.v)
		}
	case yaml.SequenceNode:
		// Items are matched by position: a list edited in place keeps its
		// per-item comments; one that grew or shrank keeps them for the
		// common prefix.
		for i := 0; i < len(fresh.Content) && i < len(old.Content); i++ {
			mergeNode(old.Content[i], fresh.Content[i])
		}
	}
}
