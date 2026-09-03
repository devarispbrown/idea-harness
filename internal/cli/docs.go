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
func cmdDocs(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "add" {
		return cmdDocsAdd(args[1:], stdout, stderr)
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	// Resolve the initiative from an optional positional id, ignoring --json.
	var listArgs []string
	for _, a := range args {
		if a != "--json" {
			listArgs = append(listArgs, a)
		}
	}
	iniID, err := resolveInitiative(c, projectID, listArgs)
	if err != nil {
		return err
	}
	var docs []cliContextDoc
	if err := c.do("GET", initiativeResource(projectID, iniID, "docs"), nil, &docs); err != nil {
		return err
	}
	if wantJSON(args) {
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
func cmdDocsAdd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: idea docs add <path|-> [--kind <k>] [--title <t>]")
	}
	var path, kind, title string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--kind" || a == "--title":
			name := a
			i++
			if i >= len(args) {
				return fmt.Errorf("%s requires a value", name)
			}
			if name == "--kind" {
				kind = args[i]
			} else {
				title = args[i]
			}
		case strings.HasPrefix(a, "--kind="):
			kind = strings.TrimPrefix(a, "--kind=")
		case strings.HasPrefix(a, "--title="):
			title = strings.TrimPrefix(a, "--title=")
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q", a)
		default:
			if path != "" {
				return fmt.Errorf("unexpected argument %q", a)
			}
			path = a
		}
	}
	if path == "" {
		return fmt.Errorf("usage: idea docs add <path|-> [--kind <k>] [--title <t>]")
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
			return fmt.Errorf("stdin exceeds the %d MiB limit", maxDocUploadBytes>>20)
		}
		content := string(data)
		if strings.TrimSpace(content) == "" {
			return fmt.Errorf("stdin is empty")
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
			return fmt.Errorf("document %q exceeds the %d MiB limit", path, maxDocUploadBytes>>20)
		}
		if len(data) == 0 {
			return fmt.Errorf("document %q is empty", path)
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
