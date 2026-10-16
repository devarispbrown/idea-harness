package cli

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureInitiatives registers a POST /initiatives handler that records the raw
// body and Content-Type and answers 202 with a canned initiative/operation id,
// so a test can assert what the CLI sent (JSON vs multipart, and the fields).
func captureInitiatives(f *fakeDaemon) (*string, *string) {
	var body, ct string
	f.mux.HandleFunc("POST /api/v1/projects/{id}/initiatives", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		ct = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"initiative_id": "ini-1", "operation_id": "op-1"})
	})
	return &body, &ct
}

func TestSplitBrief(t *testing.T) {
	cases := []struct {
		in    string
		title string
	}{
		{"Build usage-based billing", "Build usage-based billing"},
		{"A recipe app\nfor home cooks\nwith sharing", "A recipe app"},
		{"\n\n  First line  \nsecond", "First line"},
	}
	for _, c := range cases {
		title, desc := splitBrief(c.in)
		if title != c.title {
			t.Errorf("splitBrief(%q) title = %q, want %q", c.in, title, c.title)
		}
		if desc != strings.TrimSpace(c.in) {
			t.Errorf("splitBrief(%q) description = %q, want %q", c.in, desc, strings.TrimSpace(c.in))
		}
	}
}

// TestNewFromFile: --file turns the file's first non-empty line into the title
// and the whole text into the description, sent as JSON when no docs attach.
func TestNewFromFile(t *testing.T) {
	f := newFakeDaemon(t)
	body, _ := captureInitiatives(f)

	dir := t.TempDir()
	brief := filepath.Join(dir, "brief.md")
	if err := os.WriteFile(brief, []byte("A recipe app\nfor home cooks who share meals\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := run(t, "new", "--file", brief, "--no-wait"); err != nil {
		t.Fatalf("idea new --file: %v", err)
	}
	var got struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(*body), &got); err != nil {
		t.Fatalf("body was not JSON: %s (%v)", *body, err)
	}
	if got.Title != "A recipe app" {
		t.Errorf("title = %q, want %q", got.Title, "A recipe app")
	}
	if got.Description != "A recipe app\nfor home cooks who share meals" {
		t.Errorf("description = %q, want the full brief", got.Description)
	}
}

// TestNewDocSendsMultipart: any --doc attaches as a multipart `file` part so
// the daemon can sniff text, PDF/DOCX/XLSX and images in the one gesture.
func TestNewDocSendsMultipart(t *testing.T) {
	f := newFakeDaemon(t)
	body, ct := captureInitiatives(f)

	dir := t.TempDir()
	notes := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(notes, []byte("# notes\n\ngrounding"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := run(t, "new", "Build usage-based billing", "--doc", notes, "--no-wait"); err != nil {
		t.Fatalf("idea new --doc: %v", err)
	}
	if !strings.HasPrefix(*ct, "multipart/form-data") {
		t.Fatalf("expected multipart, got Content-Type %q", *ct)
	}
	fields, files := parseMultipart(t, *ct, *body)
	if fields["title"] != "Build usage-based billing" {
		t.Errorf("title field = %q", fields["title"])
	}
	if _, ok := files["notes.md"]; !ok {
		t.Errorf("expected a file part named notes.md, got files=%v", files)
	} else if !strings.Contains(files["notes.md"], "grounding") {
		t.Errorf("file part did not carry the file bytes: %q", files["notes.md"])
	}
}

// parseMultipart decodes a multipart body into its form fields and file parts.
func parseMultipart(t *testing.T, ct, body string) (map[string]string, map[string]string) {
	t.Helper()
	_, params, err := mime.ParseMediaType(ct)
	if err != nil {
		t.Fatalf("parse media type: %v", err)
	}
	mr := multipart.NewReader(strings.NewReader(body), params["boundary"])
	fields := map[string]string{}
	files := map[string]string{}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next part: %v", err)
		}
		data, _ := io.ReadAll(p)
		if p.FileName() != "" {
			files[p.FileName()] = string(data)
		} else {
			fields[p.FormName()] = string(data)
		}
	}
	return fields, files
}
