package daemon

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idea-harness/idea-harness/internal/domain"
)

// newTestProjectInitiative registers a project and creates one initiative,
// returning their ids.
func newTestProjectInitiative(t *testing.T, rt *Runtime, ts *httptest.Server, token string) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects", token, "", `{"root_path":"`+dir+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create project: %d", resp.StatusCode)
	}
	var p map[string]string
	json.NewDecoder(resp.Body).Decode(&p)
	resp.Body.Close()

	pr, err := rt.EnsureProject(t.Context(), p["project_id"])
	if err != nil {
		t.Fatal(err)
	}
	ini := &domain.Initiative{Title: "docs", Description: "x"}
	if err := pr.db.CreateInitiative(t.Context(), ini); err != nil {
		t.Fatal(err)
	}
	return p["project_id"], ini.ID
}

func postMultipartDoc(t *testing.T, ts *httptest.Server, token, projectID, iniID, filename string, fileContent string, fields map[string]string) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(fileContent)); err != nil {
		t.Fatal(err)
	}
	mw.Close()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/docs", &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Idea-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAddDocMultipart(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)

	// Multipart .md upload: kind provided, title defaults to the filename.
	resp := postMultipartDoc(t, ts, token, projectID, iniID, "my-prd.md", "# PRD\n\nGrounding", map[string]string{"kind": "prd"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("multipart upload: %d", resp.StatusCode)
	}
	var doc domain.ContextDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if doc.Kind != "prd" || doc.Title != "my-prd.md" {
		t.Fatalf("doc = %+v, want kind=prd title=my-prd.md", doc)
	}

	// The row persisted with the extracted text.
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := pr.db.ListContextDocs(t.Context(), iniID)
	if err != nil || len(docs) != 1 {
		t.Fatalf("expected 1 persisted doc, got %d (err=%v)", len(docs), err)
	}
	if !strings.Contains(docs[0].Preview, "Grounding") {
		t.Fatalf("preview = %q, want to contain extracted text", docs[0].Preview)
	}
}

func TestAddDocJSONPasteDefaultsTitle(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)

	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/docs", token, "", `{"content":"# PRD\n\ntext","kind":"prd"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("json paste: %d", resp.StatusCode)
	}
	var doc domain.ContextDoc
	json.NewDecoder(resp.Body).Decode(&doc)
	resp.Body.Close()
	if doc.Title != "untitled" {
		t.Fatalf("title = %q, want %q", doc.Title, "untitled")
	}
}

func TestAddDocImage(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)

	// A PNG upload must be stored as an image doc (not text-extracted).
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	resp := postMultipartDoc(t, ts, token, projectID, iniID, "mockup.png", string(png), map[string]string{"kind": "design"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("image upload: %d", resp.StatusCode)
	}
	var doc domain.ContextDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if doc.Format != "image" {
		t.Fatalf("format = %q, want image", doc.Format)
	}
	if doc.Digest == "" {
		t.Fatal("expected an image blob digest")
	}
}

func TestAddDocRejectsCredential(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)

	const secret = "sk_live_4eC39HqLyjWDarjtT1zdp7dc"
	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/docs", token, "", `{"content":"key=`+secret+`","kind":"other"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("credential upload should 400, got %d", resp.StatusCode)
	}
	body := new(bytes.Buffer)
	body.ReadFrom(resp.Body)
	resp.Body.Close()
	if strings.Contains(body.String(), secret) {
		t.Fatalf("error response echoed the credential: %s", body.String())
	}
}
