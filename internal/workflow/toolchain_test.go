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

// TestDetectCheckCommandsNothingRecognized is defect #5's fail-closed half:
// no recognized marker means no guess at all, same as detectToolchainImage.
func TestDetectCheckCommandsNothingRecognized(t *testing.T) {
	build, test := detectCheckCommands(t.TempDir())
	if build.Cmd != "" || test.Cmd != "" {
		t.Fatalf("build=%+v test=%+v, want both empty with no markers", build, test)
	}
}

// TestDetectCheckCommandsGo is defect #5 from the ICP re-run: "check: {}"
// stayed empty for an obvious go.mod project even though the plan (C5 /
// toolchain) expected build/test commands to be detected from it.
func TestDetectCheckCommandsGo(t *testing.T) {
	root := t.TempDir()
	writeToolchainFile(t, root, "go.mod", "module example.com/x\n\ngo 1.24\n")
	build, test := detectCheckCommands(root)
	if build.Cmd != "go build ./..." || build.Source != "go.mod" {
		t.Fatalf("build = %+v, want {go build ./... go.mod}", build)
	}
	if test.Cmd != "go test ./..." || test.Source != "go.mod" {
		t.Fatalf("test = %+v, want {go test ./... go.mod}", test)
	}
}

// TestDetectCheckCommandsNode covers package.json scripts.test/scripts.build,
// and that the detected package manager follows the lockfile present.
func TestDetectCheckCommandsNode(t *testing.T) {
	for _, c := range []struct {
		name     string
		lockfile string
		wantPM   string
		hasBuild bool
	}{
		{"npm default (no lockfile)", "", "npm", true},
		{"pnpm lockfile", "pnpm-lock.yaml", "pnpm", true},
		{"yarn lockfile", "yarn.lock", "yarn", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			scripts := `"test": "vitest run"`
			if c.hasBuild {
				scripts = `"build": "vite build", ` + scripts
			}
			writeToolchainFile(t, root, "package.json", `{"scripts": {`+scripts+`}}`)
			if c.lockfile != "" {
				writeToolchainFile(t, root, c.lockfile, "")
			}
			build, test := detectCheckCommands(root)
			if test.Cmd != c.wantPM+" test" || test.Source != "package.json scripts.test" {
				t.Errorf("test = %+v, want {%s test package.json scripts.test}", test, c.wantPM)
			}
			if c.hasBuild {
				if build.Cmd != c.wantPM+" run build" || build.Source != "package.json scripts.build" {
					t.Errorf("build = %+v, want {%s run build package.json scripts.build}", build, c.wantPM)
				}
			} else if build.Cmd != "" {
				t.Errorf("build = %+v, want empty: no scripts.build entry", build)
			}
		})
	}
}

// TestDetectCheckCommandsPython covers pyproject.toml -> pytest (test only;
// there is no unambiguous build step to guess for a Python project).
func TestDetectCheckCommandsPython(t *testing.T) {
	root := t.TempDir()
	writeToolchainFile(t, root, "pyproject.toml", "[project]\nname = \"x\"\n")
	build, test := detectCheckCommands(root)
	if build.Cmd != "" {
		t.Errorf("build = %+v, want empty for a Python project", build)
	}
	if test.Cmd != "pytest" || test.Source != "pyproject.toml" {
		t.Fatalf("test = %+v, want {pytest pyproject.toml}", test)
	}
}

// TestDetectCheckCommandsCargo covers Cargo.toml -> cargo build/cargo test.
func TestDetectCheckCommandsCargo(t *testing.T) {
	root := t.TempDir()
	writeToolchainFile(t, root, "Cargo.toml", "[package]\nname = \"x\"\n")
	build, test := detectCheckCommands(root)
	if build.Cmd != "cargo build" || build.Source != "Cargo.toml" {
		t.Fatalf("build = %+v, want {cargo build Cargo.toml}", build)
	}
	if test.Cmd != "cargo test" || test.Source != "Cargo.toml" {
		t.Fatalf("test = %+v, want {cargo test Cargo.toml}", test)
	}
}

// TestDetectCheckCommandsAmbiguousAcrossToolchains fails closed, same as
// detectToolchainImage, rather than guessing between two recognized markers.
func TestDetectCheckCommandsAmbiguousAcrossToolchains(t *testing.T) {
	root := t.TempDir()
	writeToolchainFile(t, root, "go.mod", "module example.com/x\n\ngo 1.24\n")
	writeToolchainFile(t, root, "Cargo.toml", "[package]\nname = \"x\"\n")
	build, test := detectCheckCommands(root)
	if build.Cmd != "" || test.Cmd != "" {
		t.Fatalf("build=%+v test=%+v, want both empty: two distinct toolchains must fail closed", build, test)
	}
}
