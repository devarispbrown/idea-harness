package workflow

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/blob"
	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/fsdiff"
	"github.com/devarispbrown/idea-harness/internal/security"
)

// TestAnswerTextReadsTheRedactedStore: an answer is written to the redacted
// store (handleAnswerQuestion, storeBlob) and must be read back from it. The
// daemon wires two DIFFERENT stores -- blobs/ and raw-blobs/ -- while
// newTestEngine wires one store as both, which is how reading the raw store
// here went unnoticed: in the daemon every answer rendered "(answer not
// found)". This test separates them the way the daemon does.
func TestAnswerTextReadsTheRedactedStore(t *testing.T) {
	eng, _ := newTestEngine(t, scriptedProvider(), config.HarnessConfig{Network: config.NetworkDisabled})
	raw, err := blob.New(filepath.Join(t.TempDir(), "raw-blobs"), nil)
	if err != nil {
		t.Fatal(err)
	}
	eng.SetRawBlobs(raw)

	digest := eng.storeBlob("use a YAML config, not JSON")
	if digest == "" {
		t.Fatal("storeBlob returned no digest")
	}
	q := domain.Question{Question: "config format?", Status: domain.QuestionResolved, AnswerDigest: digest}
	if got := eng.answerText(q); got != "use a YAML config, not JSON" {
		t.Fatalf("answerText = %q, want the stored answer", got)
	}
	if ok, _ := raw.Exists(digest); ok {
		t.Fatal("the answer reached the raw store; it belongs in the redacted one")
	}
}

// TestRawBlobContentIsRedactedAtEveryDisplaySurface: the raw store keeps file
// content byte-for-byte (apply writes it, revert restores it, and both are
// keyed on the digest of the real bytes), so a credential a human allowlisted
// "not a secret" -- or one in a pre-image apply captured from the user's own
// workspace -- sits there unmasked. Every engine path that turns raw content
// into something an API returns must mask it: the rendered patch, the
// structured diff's content and its hunk, and the base side of that hunk.
func TestRawBlobContentIsRedactedAtEveryDisplaySurface(t *testing.T) {
	eng, _ := newTestEngine(t, scriptedProvider(), config.HarnessConfig{Network: config.NetworkDisabled})
	eng.SetRedactor(security.NewRedactor())
	key := "sk-ant-api03-" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789ABCDEF"

	base := []byte("const old = \"" + key + "\";\n")
	post := []byte("const key = \"" + key + "\";\nexport default key;\n")
	baseDigest, err := eng.contentBlobs().Put(base)
	if err != nil {
		t.Fatal(err)
	}
	postDigest, err := eng.contentBlobs().Put(post)
	if err != nil {
		t.Fatal(err)
	}
	patchDigest, err := eng.persistPatch(fsdiff.Patch{Files: []domain.FileChange{{
		Path: "src/key.js", Digest: postDigest, BaseDigest: baseDigest, ChangeType: domain.FileModified,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{PatchDigest: patchDigest}

	if got := eng.RenderPatch(cs); strings.Contains(got, key) {
		t.Errorf("RenderPatch served the raw credential:\n%s", got)
	}
	for _, sf := range eng.StructuredDiff(cs) {
		if strings.Contains(sf.Content, key) || strings.Contains(sf.Patch, key) {
			t.Errorf("StructuredDiff served the raw credential: %+v", sf)
		}
	}
	// And the store itself is untouched: apply needs the real bytes.
	got, err := eng.contentBlobs().Get(postDigest)
	if err != nil || string(got) != string(post) {
		t.Fatalf("raw store no longer round-trips byte-for-byte: %q, %v", got, err)
	}
}
