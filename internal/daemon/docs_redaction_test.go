package daemon

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestAddDocDocxWithCredentialIsRejected: text extracted from a binary office
// format goes through the same sweep a pasted doc does. The credential sits in
// the docx's XML, not in anything the upload's own bytes would show a naive
// string scan, so this proves the sweep runs on the EXTRACTED text.
func TestAddDocDocxWithCredentialIsRejected(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)

	key := "AKIA" + "Q3EGRIXJ7PZ4WN2T"
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>Deploy notes: access key `+key+`</w:t></w:r></w:p>
</w:body></w:document>`)
	zw.Close()

	resp := postMultipartDoc(t, ts, token, projectID, iniID, "notes.docx", buf.String(), nil)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("docx carrying an AWS key: status %d, want 400 (%s)", resp.StatusCode, body)
	}
	if strings.Contains(string(body), key) {
		t.Fatalf("the rejection echoed the key: %s", body)
	}
}

// TestAddDocImageTitleIsRedacted: an image's bytes are stored verbatim (they
// cannot be text-redacted), but its title and kind are text that is listed by
// the API and embedded in prompts, so a key in the filename is masked.
func TestAddDocImageTitleIsRedacted(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)

	key := "sk-ant-api03-" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789ABCDEF"
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	resp := postMultipartDoc(t, ts, token, projectID, iniID, key+".png", string(png), nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("image upload: %d", resp.StatusCode)
	}
	var doc domain.ContextDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(doc.Title, key) {
		t.Fatalf("image title kept the key: %q", doc.Title)
	}

	// Let the async describe goroutine finish before TempDir cleanup.
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got, err := pr.db.GetContextDoc(t.Context(), doc.ID); err == nil && got.DescriptionDigest != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}
