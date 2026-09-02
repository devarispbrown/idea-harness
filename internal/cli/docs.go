package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/docs", nil, &docs); err != nil {
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

// cmdDocsAdd uploads a file as a context doc.
func cmdDocsAdd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: idea docs add <path> [--kind <k>] [--title <t>]")
	}
	path := args[0]
	kind := ""
	title := ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--kind":
			if i+1 < len(args) {
				kind = args[i+1]
				i++
			}
		case "--title":
			if i+1 < len(args) {
				title = args[i+1]
				i++
			}
		}
	}

	if fi, err := os.Stat(path); err == nil && fi.Size() > maxDocUploadBytes {
		return fmt.Errorf("document %q exceeds the %d MiB limit", path, maxDocUploadBytes>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(data)
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("document %q is empty", path)
	}
	if kind == "" {
		kind = inferDocKind(path)
	}
	if title == "" {
		title = filepath.Base(path)
	}

	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, nil)
	if err != nil {
		return err
	}
	var doc cliContextDoc
	if err := c.do("POST", "/api/v1/projects/"+projectID+"/initiatives/"+iniID+"/docs",
		map[string]string{"content": content, "kind": kind, "title": title}, &doc); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ doc added")+" "+code(doc.Kind)+" "+bold(doc.Title))
	return nil
}

// inferDocKind guesses a context-doc kind from the file's base name.
func inferDocKind(path string) string {
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.Contains(base, "prd"):
		return "prd"
	case strings.Contains(base, "design"):
		return "design"
	case strings.Contains(base, "tech"):
		return "tech"
	case strings.Contains(base, "market"):
		return "market"
	default:
		return "other"
	}
}
