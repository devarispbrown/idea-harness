package quality

import (
	"fmt"
	"regexp"
	"strings"
)

// SetupCommand is one documented setup/build/test command, where it was
// documented, and -- when Skip is set -- why the offline reproducibility run
// does not execute it.
type SetupCommand struct {
	Cmd  string `json:"cmd"`
	Path string `json:"path"` // README.md, Makefile, package.json, .idea-harness/harness.yaml
	Line int    `json:"line,omitempty"`
	// Skip is "" for a command that runs, or the reason it does not: it
	// fetches dependencies (needs the network), or it starts something
	// long-running (a dev server never exits).
	Skip string `json:"skip,omitempty"`
}

// Source renders where a command came from, path:line.
func (c SetupCommand) Source() string {
	if c.Line > 0 {
		return fmt.Sprintf("%s:%d", c.Path, c.Line)
	}
	return c.Path
}

// setupSectionRe names the README sections whose shell blocks are setup,
// build or test instructions. Usage/running sections are deliberately not
// read: their commands start servers and never exit.
var setupSectionRe = regexp.MustCompile(`(?i)\b(install|installation|installing|setup|set up|getting started|quick ?start|build|building|test|tests|testing|develop|development|contributing|from source|prerequisites|requirements)\b`)

// shellFenceLangs are the fence info strings read as shell. An unlabelled
// fence counts too (READMEs leave the label off often enough that ignoring
// them would miss the common case).
var shellFenceLangs = map[string]bool{"": true, "sh": true, "bash": true, "shell": true, "console": true, "zsh": true, "terminal": true, "shell-session": true, "shellsession": true}

// knownTools are the first words a documented command may start with to be
// run. Anything else -- prose that slipped into a fence, `export`, an editor
// -- is not a build step and is ignored rather than "failing".
var knownTools = map[string]bool{
	"make": true, "go": true, "npm": true, "npx": true, "pnpm": true, "yarn": true, "node": true, "bun": true, "deno": true,
	"pip": true, "pip3": true, "python": true, "python3": true, "pytest": true, "poetry": true, "uv": true, "pipenv": true, "tox": true, "nox": true,
	"cargo": true, "rustc": true, "bundle": true, "rake": true, "ruby": true, "mvn": true, "gradle": true, "dotnet": true,
	"cmake": true, "meson": true, "ninja": true, "just": true, "task": true, "sh": true, "bash": true,
	"git": true, "curl": true, "wget": true, "brew": true, "apt": true, "apt-get": true, "apk": true, "docker": true, "docker-compose": true, "cd": true,
}

var (
	fetchCmdRe = regexp.MustCompile(`^(?:(?:npm|pnpm|bun)\s+(?:install|i|ci|add)\b|yarn(?:\s+(?:install|add)\b|\s*$)|(?:pip3?|python3?\s+-m\s+pip)\s+install\b|poetry\s+install\b|uv\s+(?:sync|pip\s+install)\b|pipenv\s+install\b|go\s+(?:mod\s+download|get)\b|go\s+install\s+\S+@|cargo\s+(?:fetch|install)\b|bundle(?:\s+install)?\s*$|gem\s+install\b|brew\b|apt(?:-get)?\b|apk\b|curl\b|wget\b|git\s+(?:clone|submodule|pull|fetch)\b)`)
	longRunningRe = regexp.MustCompile(`^(?:(?:npm|pnpm|yarn|bun)\s+(?:run\s+)?(?:start|dev|serve|watch)\b|go\s+run\b|flask\s+run\b|uvicorn\b|gunicorn\b|python3?\s+manage\.py\s+runserver\b|node\s+\S*(?:server|index|app|main)\S*\s*$|make\s+(?:run|dev|serve|start|watch)\b|docker(?:-compose)?\b|cargo\s+run\b|dotnet\s+run\b)`)
	envAssignRe   = regexp.MustCompile(`^(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)+`)
	promptRe      = regexp.MustCompile(`^(?:\$|%|>|#)\s+`)
	makeTargetRe  = regexp.MustCompile(`(?m)^(build|test|check|all|install|setup)\s*:`)
)

// ReadmeCommands extracts the shell commands documented in a README's
// setup/build/test sections, in order, each with the README line it is on.
//
// It follows `cd <dir>` within one fenced block (later commands in that
// block run in <dir>) when dirExists says the directory is part of the
// workspace; a `cd` into a directory that is not -- the conventional
// `git clone ... && cd project` -- is dropped, since the fresh copy already
// is that directory.
func ReadmeCommands(readmePath, text string, dirExists func(string) bool) []SetupCommand {
	lines := strings.Split(text, "\n")
	var out []SetupCommand
	inSection, inFence, fenceShell, consoleFence := false, false, false, false
	blockDir := ""
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		t := strings.TrimSpace(raw)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			if inFence {
				inFence = false
				continue
			}
			inFence = true
			lang := strings.ToLower(strings.TrimSpace(strings.TrimLeft(t, "`~")))
			lang, _, _ = strings.Cut(lang, " ")
			fenceShell = shellFenceLangs[lang]
			consoleFence = lang == "console" || lang == "shell-session" || lang == "shellsession"
			blockDir = ""
			continue
		}
		if !inFence {
			if m := atxHeadingRe.FindStringSubmatch(raw); m != nil {
				inSection = setupSectionRe.MatchString(m[1])
			} else if i+1 < len(lines) && t != "" {
				next := strings.TrimSpace(lines[i+1])
				if len(next) >= 3 && (strings.Trim(next, "=") == "" || strings.Trim(next, "-") == "") {
					inSection = setupSectionRe.MatchString(t)
				}
			}
			continue
		}
		if !inSection || !fenceShell || t == "" {
			continue
		}
		line := t
		hadPrompt := promptRe.MatchString(line) && !strings.HasPrefix(line, "# ")
		if consoleFence && !hadPrompt {
			continue // console output, not a command
		}
		if strings.HasPrefix(line, "#") && !hadPrompt {
			continue // a comment
		}
		line = promptRe.ReplaceAllString(line, "")
		lineNo := i + 1
		for strings.HasSuffix(line, "\\") && i+1 < len(lines) {
			i++
			line = strings.TrimSpace(strings.TrimSuffix(line, "\\")) + " " + strings.TrimSpace(lines[i])
		}
		for _, part := range splitAnd(line) {
			cmd := strings.TrimSpace(part)
			if cmd == "" {
				continue
			}
			bare := envAssignRe.ReplaceAllString(cmd, "")
			first, _, _ := strings.Cut(bare, " ")
			if first == "cd" {
				dir := strings.TrimSpace(strings.TrimPrefix(bare, "cd"))
				if dir != "" && dirExists != nil && dirExists(joinDir(blockDir, dir)) {
					blockDir = joinDir(blockDir, dir)
				}
				continue
			}
			if !knownTools[first] && !strings.HasPrefix(first, "./") && !strings.HasPrefix(first, "scripts/") && !strings.HasPrefix(first, "bin/") {
				continue
			}
			c := SetupCommand{Cmd: cmd, Path: readmePath, Line: lineNo}
			switch {
			case fetchCmdRe.MatchString(bare):
				c.Skip = "fetches dependencies or tools, which needs the network (list it under verify.setup.prepare to run it in the network-enabled prepare phase)"
			case longRunningRe.MatchString(bare):
				c.Skip = "starts a long-running process or needs Docker, which a reproducibility run cannot wait for"
			case first == "git":
				c.Skip = "operates on a git checkout; the fresh copy is a plain directory"
			}
			if blockDir != "" {
				c.Cmd = "cd " + shellQuote(blockDir) + " && " + cmd
			}
			out = append(out, c)
		}
	}
	return out
}

// MakefileCommands returns `make <target>` for the conventional build/test
// targets a Makefile declares, in a fixed order (build before test).
func MakefileCommands(makefile string) []SetupCommand {
	have := map[string]int{}
	for _, m := range makeTargetRe.FindAllStringSubmatchIndex(makefile, -1) {
		name := makefile[m[2]:m[3]]
		if _, ok := have[name]; !ok {
			have[name] = lineOf(makefile, m[0])
		}
	}
	var out []SetupCommand
	for _, t := range []string{"build", "test"} {
		if l, ok := have[t]; ok {
			out = append(out, SetupCommand{Cmd: "make " + t, Path: "Makefile", Line: l})
		}
	}
	return out
}

// splitAnd splits a command line on top-level `&&` (outside quotes), so
// `npm ci && npm test` becomes two commands with their own skip decisions.
func splitAnd(s string) []string {
	var out []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '&' && i+1 < len(s) && s[i+1] == '&':
			out = append(out, cur.String())
			cur.Reset()
			i++
			continue
		}
		cur.WriteByte(c)
	}
	return append(out, cur.String())
}

func joinDir(base, dir string) string {
	dir = strings.Trim(dir, `"'`)
	dir = strings.TrimPrefix(dir, "./")
	if base == "" {
		return strings.TrimSuffix(dir, "/")
	}
	return strings.TrimSuffix(base+"/"+dir, "/")
}

func shellQuote(s string) string {
	if regexp.MustCompile(`^[A-Za-z0-9_./-]+$`).MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
