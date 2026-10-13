package ciguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestReleaseIsSignedAttestedAndHasSBOMs pins the supply-chain half of the
// release (deferred.md, audit 7 / K4) against the quiet ways it comes undone:
// a permission dropped from the job (keyless signing then fails at release
// time, never in a PR), the tools goreleaser shells out to no longer installed,
// or a block deleted from .goreleaser.yaml so a release ships unsigned and
// nothing says so.
func TestReleaseIsSignedAttestedAndHasSBOMs(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	release := wf.Jobs[releaseJobID]
	for scope, want := range map[string]string{"contents": "write", "id-token": "write", "attestations": "write"} {
		if got := release.Permissions[scope]; got != want {
			t.Errorf("the %q job's %s permission is %q, want %q", releaseJobID, scope, got, want)
		}
	}
	// id-token: write lets a job mint an OIDC token that Sigstore and the
	// attestation service accept as this repository's workflow. Only the job
	// that publishes may hold it -- in particular never the agent tier, which
	// runs a coding agent with network egress.
	for id, job := range wf.Jobs {
		if id != releaseJobID && job.Permissions["id-token"] != "" {
			t.Errorf("job %q declares id-token: %s; only the release job may mint an OIDC token",
				id, job.Permissions["id-token"])
		}
	}

	cosign, syft, attest, goreleaser := -1, -1, -1, -1
	for i, st := range release.Steps {
		switch {
		case strings.HasPrefix(st.Uses, "sigstore/cosign-installer@"):
			cosign = i
		case strings.HasPrefix(st.Uses, "anchore/sbom-action/download-syft@"):
			syft = i
		case strings.HasPrefix(st.Uses, "actions/attest-build-provenance@"):
			attest = i
			if !strings.Contains(st.With["subject-path"], "dist/") {
				t.Errorf("the attestation step does not attest goreleaser's dist/ output: %q", st.With["subject-path"])
			}
		case strings.HasPrefix(st.Uses, "goreleaser/goreleaser-action@"):
			goreleaser = i
			if _, ok := st.Env["HOMEBREW_TAP_GITHUB_TOKEN"]; !ok {
				t.Error("goreleaser is not given HOMEBREW_TAP_GITHUB_TOKEN, so the cask can never be pushed")
			}
		}
	}
	if goreleaser < 0 {
		t.Fatal("the release job no longer runs goreleaser")
	}
	if cosign < 0 || cosign > goreleaser {
		t.Error("cosign is not installed before goreleaser runs; the `signs` block would fail")
	}
	if syft < 0 || syft > goreleaser {
		t.Error("syft is not installed before goreleaser runs; the `sboms` block would fail")
	}
	if attest < goreleaser {
		t.Error("the build-provenance attestation must run after goreleaser, which writes the files it attests")
	}

	raw, err = os.ReadFile(filepath.Join(root, ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var gr struct {
		Signs []struct {
			Cmd       string `yaml:"cmd"`
			Artifacts string `yaml:"artifacts"`
		} `yaml:"signs"`
		SBOMs []struct {
			Artifacts string `yaml:"artifacts"`
		} `yaml:"sboms"`
		Casks []struct {
			Repository struct {
				Owner string `yaml:"owner"`
				Name  string `yaml:"name"`
			} `yaml:"repository"`
		} `yaml:"homebrew_casks"`
	}
	if err := yaml.Unmarshal(raw, &gr); err != nil {
		t.Fatal(err)
	}
	signed := false
	for _, s := range gr.Signs {
		signed = signed || (s.Cmd == "cosign" && s.Artifacts == "checksum")
	}
	if !signed {
		t.Error(".goreleaser.yaml no longer signs checksums.txt with cosign")
	}
	sbom := false
	for _, s := range gr.SBOMs {
		sbom = sbom || s.Artifacts == "archive"
	}
	if !sbom {
		t.Error(".goreleaser.yaml no longer generates an SBOM per archive")
	}
	if len(gr.Casks) != 1 || gr.Casks[0].Repository.Owner != "devarispbrown" || gr.Casks[0].Repository.Name != "homebrew-tap" {
		t.Errorf(".goreleaser.yaml's homebrew cask does not target devarispbrown/homebrew-tap: %+v", gr.Casks)
	}
}
