package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/docparse"
)

// cliContextDoc mirrors the daemon's context-doc response.
type cliContextDoc struct {
	ID           string `json:"id"`
	InitiativeID string `json:"initiative_id"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	Digest       string `json:"digest"`
	Preview      string `json:"preview"`
	CreatedAt    string `json:"created_at"`
}

// cmdDocs lists uploaded context docs, or dispatches to `docs add`.
func cmdDocs(a cmdArgs, stdout, stderr io.Writer) error {
	if a.sub == "add" {
		return cmdDocsAdd(a, stdout, stderr)
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	// Flags are off the args by the time dispatch calls this, so the positional
	// is just the optional initiative id. This used to strip "--json" from the
	// slice by hand and pass the remainder through, so `idea docs --jsn` looked
	// for an initiative called "--jsn" (DX audit).
	iniID, err := resolveInitiative(c, projectID, a.positional)
	if err != nil {
		return err
	}
	var docs []cliContextDoc
	if err := c.do("GET", initiativeResource(projectID, iniID, "docs"), nil, &docs); err != nil {
		return err
	}
	if a.has("--json") {
		return dumpJSON(stdout, docs)
	}
	if len(docs) == 0 {
		fmt.Fprintln(stdout, muted("No context docs yet. Add one with:")+" "+bold("idea docs add <path>"))
		return nil
	}
	fmt.Fprintln(stdout, heading("Context docs"))
	for _, d := range docs {
		fmt.Fprintf(stdout, "  %s %s\n", code(d.Kind), bold(d.Title))
		if d.Preview != "" {
			fmt.Fprintln(stdout, "      "+muted(d.Preview))
		}
	}
	return nil
}

// maxDocUploadBytes mirrors the daemon's upload cap so the CLI rejects
// oversized files before reading them into memory.
const maxDocUploadBytes = 2 << 20

// cmdDocsAdd uploads a file (or stdin via `-`) as a context doc. Files are sent
// as multipart so the daemon can extract text from .md/.txt/.pdf/.docx/.xlsx;
// stdin is sent as JSON paste-text. Flags may precede the path and accept both
// `--kind v` and `--kind=v`.
func cmdDocsAdd(a cmdArgs, stdout, stderr io.Writer) error {
	// The parser already carves out a bare "-" as a positional, precisely so
	// this command can read from stdin. cmdDocsAdd rolled its own loop instead
	// and rejected "-" as an unknown flag before reaching the positional case --
	// so `idea docs add -`, documented in README, docs/ui.md, the usage string
	// and as a literal copy-paste line in docs/quickstart.md, never worked.
	path, kind, title := a.arg(0), a.value("--kind"), a.value("--title")
	if path == "" {
		return usageErrorf("usage: idea docs add <path|-> [--kind <k>] [--title <t>]")
	}
	if len(a.positional) > 1 {
		return usageErrorf("unexpected argument %q", a.positional[1])
	}

	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, nil)
	if err != nil {
		return err
	}
	endpoint := initiativeResource(projectID, iniID, "docs")
	var doc cliContextDoc

	if path == "-" {
		// Paste-text fallback: read stdin, bounded, and send as JSON.
		data, err := io.ReadAll(io.LimitReader(os.Stdin, maxDocUploadBytes+1))
		if err != nil {
			return err
		}
		if len(data) > maxDocUploadBytes {
			return usageErrorf("stdin exceeds the %d MiB limit", maxDocUploadBytes>>20)
		}
		content := string(data)
		if strings.TrimSpace(content) == "" {
			return usageErrorf("stdin is empty")
		}
		if kind == "" {
			kind = "other"
		}
		if title == "" {
			title = "stdin"
		}
		if err := c.do("POST", endpoint, map[string]string{"content": content, "kind": kind, "title": title}, &doc); err != nil {
			return err
		}
	} else {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(f, maxDocUploadBytes+1))
		f.Close()
		if err != nil {
			return err
		}
		if len(data) > maxDocUploadBytes {
			return usageErrorf("document %q exceeds the %d MiB limit", path, maxDocUploadBytes>>20)
		}
		if len(data) == 0 {
			return usageErrorf("document %q is empty", path)
		}
		if kind == "" {
			kind = docparse.InferKind(path)
		}
		if title == "" {
			title = filepath.Base(path)
		}
		if err := c.doMultipart("POST", endpoint, map[string]string{"kind": kind, "title": title}, "file", filepath.Base(path), data, &doc); err != nil {
			return err
		}
	}
	fmt.Fprintln(stdout, ok("✓ doc added")+" "+code(doc.Kind)+" "+bold(doc.Title))
	return nil
}
