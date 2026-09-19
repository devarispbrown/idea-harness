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
