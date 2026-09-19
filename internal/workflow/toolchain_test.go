package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func writeToolchainFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestDetectToolchainImageNothingRecognized(t *testing.T) {
	root := t.TempDir()
	image, evidence := detectToolchainImage(root)
	if image != "" {
		t.Fatalf("image = %q, want empty (no markers)", image)
	}
	if evidence == "" {
		t.Error("want a non-empty evidence string explaining why")
	}
}

func TestDetectToolchainImageNode(t *testing.T) {
	for _, c := range []struct {
		name, file, content, wantImage string
	}{
		{"nvmrc plain", ".nvmrc", "20.11.0", "node:20"},
		{"nvmrc v-prefixed", ".nvmrc", "v18\n", "node:18"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeToolchainFile(t, root, c.file, c.content)
			image, evidence := detectToolchainImage(root)
			if image != c.wantImage {
				t.Fatalf("image = %q, want %q (evidence: %s)", image, c.wantImage, evidence)
			}
		})
	}
}

func TestDetectToolchainImageNodeFromPackageJSON(t *testing.T) {
	root := t.TempDir()
	writeToolchainFile(t, root, "package.json", `{"name":"x","engines":{"node":"22.x"}}`)
	image, evidence := detectToolchainImage(root)
	if image != "node:22" {
		t.Fatalf("image = %q, want node:22 (evidence: %s)", image, evidence)
	}
}

func TestDetectToolchainImageNvmrcWinsOverPackageJSON(t *testing.T) {
	root := t.TempDir()
	writeToolchainFile(t, root, ".nvmrc", "20")
	writeToolchainFile(t, root, "package.json", `{"engines":{"node":"18"}}`)
	image, _ := detectToolchainImage(root)
	if image != "node:20" {
		t.Fatalf("image = %q, want node:20 (.nvmrc takes priority)", image)
	}
}

func TestDetectToolchainImageNodeRangeIsAmbiguous(t *testing.T) {
	root := t.TempDir()
	writeToolchainFile(t, root, "package.json", `{"engines":{"node":">=18.0.0 <21.0.0"}}`)
	image, evidence := detectToolchainImage(root)
	if image != "" {
		t.Fatalf("image = %q, want empty: a range must not be guessed at (evidence: %s)", image, evidence)
	}
}

func TestDetectToolchainImageNodeNonNumericTagIsUndetected(t *testing.T) {
	root := t.TempDir()
	writeToolchainFile(t, root, ".nvmrc", "lts/*")
	image, _ := detectToolchainImage(root)
	if image != "" {
		t.Fatalf("image = %q, want empty: \"lts/*\" names no version", image)
	}
}

func TestDetectToolchainImageGo(t *testing.T) {
	for _, c := range []struct {
		name, goMod, want string
	}{
		{"two-part", "module example.com/x\n\ngo 1.24\n", "golang:1.24"},
		{"three-part toolchain directive", "module example.com/x\n\ngo 1.24.3\n", "golang:1.24.3"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeToolchainFile(t, root, "go.mod", c.goMod)
			image, evidence := detectToolchainImage(root)
			if image != c.want {
				t.Fatalf("image = %q, want %q (evidence: %s)", image, c.want, evidence)
			}
		})
	}
}

func TestDetectToolchainImagePython(t *testing.T) {
	root := t.TempDir()
	writeToolchainFile(t, root, ".python-version", "3.12.1\n")
	image, evidence := detectToolchainImage(root)
	if image != "python:3.12.1" {
		t.Fatalf("image = %q, want python:3.12.1 (evidence: %s)", image, evidence)
	}
}

func TestDetectToolchainImagePythonVirtualenvQualifiedIsUndetected(t *testing.T) {
	root := t.TempDir()
	// pyenv can write a virtualenv-qualified name here; it must not be guessed.
	writeToolchainFile(t, root, ".python-version", "3.12.1/envs/myproject\n")
	image, _ := detectToolchainImage(root)
	if image != "" {
		t.Fatalf("image = %q, want empty: a virtualenv-qualified name is not a bare version", image)
	}
}

// TestDetectToolchainImageNodeRangesAreRefused is review M6: every shape of
// range -- a comparison, an OR'd set of ranges, a caret/tilde semver range --
// must fail closed exactly like an ambiguous version, never guessed at.
// Mutation-check: revert nodeVersionSpec/parseLeadingMajorVersion to the old
// TrimLeft-and-take-leading-digits approach, and every case here starts
// matching again.
func TestDetectToolchainImageNodeRangesAreRefused(t *testing.T) {
	for _, spec := range []string{
		">=24",
		"<21",
		"^18||^20",
		"^18.2.0",
		"~18.2.0",
		">=18.0.0",
	} {
		t.Run(spec, func(t *testing.T) {
			root := t.TempDir()
			writeToolchainFile(t, root, "package.json", `{"engines":{"node":"`+spec+`"}}`)
			image, evidence := detectToolchainImage(root)
			if image != "" {
				t.Fatalf("image = %q, want empty: %q is a range and must not be guessed at (evidence: %s)", image, spec, evidence)
			}
		})
	}
	// Same refusal via .nvmrc, which takes a bare specifier the same way.
	for _, spec := range []string{">=24", "^18.2.0"} {
		t.Run(".nvmrc/"+spec, func(t *testing.T) {
			root := t.TempDir()
			writeToolchainFile(t, root, ".nvmrc", spec+"\n")
			image, evidence := detectToolchainImage(root)
			if image != "" {
				t.Fatalf("image = %q, want empty: %q is a range and must not be guessed at (evidence: %s)", image, spec, evidence)
			}
		})
	}
}

// TestDetectToolchainImageNodeAcceptsUnambiguousForms is the positive
// complement: every shape nodeVersionSpec is meant to accept.
func TestDetectToolchainImageNodeAcceptsUnambiguousForms(t *testing.T) {
	for _, c := range []struct{ spec, want string }{
		{"18", "node:18"},
		{"v18", "node:18"},
		{"18.2", "node:18"},
		{"18.2.0", "node:18"},
		{"18.x", "node:18"},
		{"18.2.x", "node:18"},
	} {
		t.Run(c.spec, func(t *testing.T) {
			root := t.TempDir()
			writeToolchainFile(t, root, "package.json", `{"engines":{"node":"`+c.spec+`"}}`)
			image, evidence := detectToolchainImage(root)
			if image != c.want {
				t.Fatalf("image = %q, want %q (evidence: %s)", image, c.want, evidence)
			}
		})
	}
}

// TestDetectToolchainImageGoPrefersToolchainDirective is review M6: when
// go.mod carries both a `go` directive (the minimum required version) and a
// `toolchain` directive (what a `go` invocation actually builds with), the
// toolchain directive's version wins.
func TestDetectToolchainImageGoPrefersToolchainDirective(t *testing.T) {
	root := t.TempDir()
	writeToolchainFile(t, root, "go.mod", "module example.com/x\n\ngo 1.21\n\ntoolchain go1.24.3\n")
	image, evidence := detectToolchainImage(root)
	if image != "golang:1.24.3" {
		t.Fatalf("image = %q, want golang:1.24.3 (the toolchain directive, not the go directive) (evidence: %s)", image, evidence)
	}
}

// TestDetectToolchainImageGoFallsBackWithoutToolchainDirective confirms the
// `go` directive is still used on its own when no `toolchain` line exists.
func TestDetectToolchainImageGoFallsBackWithoutToolchainDirective(t *testing.T) {
	root := t.TempDir()
	writeToolchainFile(t, root, "go.mod", "module example.com/x\n\ngo 1.21\n")
	image, evidence := detectToolchainImage(root)
	if image != "golang:1.21" {
		t.Fatalf("image = %q, want golang:1.21 (evidence: %s)", image, evidence)
	}
}

func TestDetectToolchainImageAmbiguousAcrossToolchains(t *testing.T) {
	root := t.TempDir()
	writeToolchainFile(t, root, "go.mod", "module example.com/x\n\ngo 1.24\n")
	writeToolchainFile(t, root, ".nvmrc", "20")
	image, evidence := detectToolchainImage(root)
	if image != "" {
		t.Fatalf("image = %q, want empty: two distinct toolchains must fail closed (evidence: %s)", image, evidence)
	}
	if evidence == "" {
		t.Error("want the ambiguity explained in the evidence")
	}
}
