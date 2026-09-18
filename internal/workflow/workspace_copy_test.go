package workflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// editingAgentDocker stands in for docker. On `run` it finds the work root in
// the `-v <host>:/work` argument and does what a coding agent asked to add a
// /health route does: appends to server.js, if server.js is there. An agent
// that never saw server.js would create it, which is the defect.
const editingAgentDocker = `#!/usr/bin/env bash
case "$1" in
  run)
    prev=""
    for a in "$@"; do
      if [ "$prev" = "-v" ] && [ "${a%:/work}" != "$a" ]; then work="${a%:/work}"; fi
      prev="$a"
    done
    [ -n "$FAKE_AGENT_SEEN" ] && ls -A "$work" > "$FAKE_AGENT_SEEN"
    echo "app.get('/health', ok)" >> "$work/server.js"
    echo "cid-edit" ;;
  inspect) echo "exited 0" ;;
  logs) echo "added /health" ;;
  rm) exit 0 ;;
  *) exit 1 ;;
esac
`

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newWorkspaceFixture is an engine whose workspace is a git repository holding
// server.js with one route, and whose executor is a fake agent that edits it.
func newWorkspaceFixture(t *testing.T) (*Engine, *store.DB, string, string) {
	t.Helper()
	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "docker"), editingAgentDocker)
	if err := os.Chmod(filepath.Join(bin, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENAI_API_KEY", "sk-test-not-real-0000")

	eng, db, iniID := newExecuteFixture(t)
	ws := eng.workspaceRoot
	writeFile(t, filepath.Join(ws, "server.js"), "app.get('/', hello)\n")
	gitRun(t, ws, "init", "-q")
	gitRun(t, ws, "add", ".")
	gitRun(t, ws, "commit", "-qm", "init")
	eng.cfg.Executor = config.ExecutorConfig{Kind: config.ExecutorHermes, Model: "openai/gpt-5.6-luna", Image: "img"}
	return eng, db, iniID, ws
}

func addTask(t *testing.T, db *store.DB, iniID, title string) *domain.Task {
	t.Helper()
	task := &domain.Task{InitiativeID: iniID, Title: title, Statement: title, Priority: domain.PriorityMust}
	if err := db.CreateTask(context.Background(), task, "", nil); err != nil {
		t.Fatal(err)
	}
	return task
}

func fileChange(t *testing.T, eng *Engine, cs domain.ChangeSet, path string) domain.FileChange {
	t.Helper()
	patch, err := eng.loadPatch(&cs)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range patch.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("%s is not in the change set: %+v", path, patch.Files)
	return domain.FileChange{}
}

// The defect this exists for: the agent's work root started empty, so an
// agent asked to add a route wrote server.js from nothing, the change was
// "added", and apply deleted the user's existing route.
func TestAgentEditsTheUsersFileRatherThanReplacingIt(t *testing.T) {
	eng, db, iniID, ws := newWorkspaceFixture(t)
	addTask(t, db, iniID, "add /health")
	ctx := context.Background()

	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	f := fileChange(t, eng, cs, "server.js")
	if f.ChangeType != domain.FileModified {
		t.Errorf("server.js is %q, want modified: the agent did not see the existing file", f.ChangeType)
	}
	if f.BaseDigest == "" {
		t.Error("a modified file carries no base digest, so apply cannot tell if it changed since")
	}
	if err := eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if err := eng.ApplyChangeSet(ctx, iniID, cs.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(ws, "server.js"))
	if !strings.Contains(string(got), "app.get('/', hello)") || !strings.Contains(string(got), "/health") {
		t.Errorf("apply lost the user's route or the new one:\n%s", got)
	}
}

// The work root goes into a container with network egress. Secrets stay out,
// and what stayed out is listed on the change set, so a missing file is
// explainable.
func TestSecretsAreNotGivenToTheAgentAndTheSkipIsReported(t *testing.T) {
	eng, db, iniID, ws := newWorkspaceFixture(t)
	writeFile(t, filepath.Join(ws, ".env"), "OPENAI_API_KEY=sk-proj-abcdefghijklmnopqrstuvwxyz0123456789\n")
	writeFile(t, filepath.Join(ws, "config", "prod.json"), `{"aws_secret": "AKIAIOSFODNN7EXAMPLE"}`+"\n")
	gitRun(t, ws, "add", "-f", ".env", "config/prod.json") // tracked, even so
	gitRun(t, ws, "commit", "-qm", "secrets")
	seen := filepath.Join(t.TempDir(), "seen")
	t.Setenv("FAKE_AGENT_SEEN", seen)
	addTask(t, db, iniID, "add /health")

	cs, err := eng.Execute(context.Background(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	listing, _ := os.ReadFile(seen)
	if strings.Contains(string(listing), ".env") {
		t.Errorf("the agent's work root contained .env:\n%s", listing)
	}
	root := filepath.Join(eng.workRoot, "work", iniID)
	if _, err := os.Stat(filepath.Join(root, "config", "prod.json")); err == nil {
		t.Error("a file holding a credential was given to the agent")
	}
	findings, err := db.ListFindingsByTarget(context.Background(), "change_set", cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Category == "workspace_copy" {
			if !strings.Contains(f.Evidence, ".env") || !strings.Contains(f.Evidence, "config/prod.json") {
				t.Errorf("the skip finding does not list what was skipped: %q", f.Evidence)
			}
			return
		}
	}
	t.Error("nothing on the change set says which files the agent did not get")
}

// A file edited in the workspace after the agent ran is not overwritten.
func TestApplyRefusesAStaleBase(t *testing.T) {
	eng, db, iniID, ws := newWorkspaceFixture(t)
	addTask(t, db, iniID, "add /health")
	ctx := context.Background()
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	edited := "app.get('/', hello)\napp.get('/users', list) // written after the agent ran\n"
	writeFile(t, filepath.Join(ws, "server.js"), edited)

	if err := eng.ApplyChangeSet(ctx, iniID, cs.ID); err == nil {
		t.Fatal("apply overwrote a file edited after the agent ran")
	}
	if got, _ := os.ReadFile(filepath.Join(ws, "server.js")); string(got) != edited {
		t.Errorf("the workspace file was written:\n%s", got)
	}
	got, _ := db.GetChangeSet(ctx, cs.ID)
	if got.Status != domain.ChangeSetConflicted {
		t.Errorf("status %q, want conflicted", got.Status)
	}
	// Retrying the same patch fails the same way: the documented trap, which
	// is why the finding says to reject and re-run.
	if err := eng.ResetConflictedForRetry(ctx, iniID, cs.ID); err != nil {
		t.Fatal(err)
	}
	if err := eng.ApplyChangeSet(ctx, iniID, cs.ID); err == nil {
		t.Error("a retry of a stale-base change set applied")
	}
}

// Task 2 must start from task 1's changes, so it cannot run while task 1's
// change set is still open.
func TestNextTaskWaitsForTheOpenChangeSet(t *testing.T) {
	eng, db, iniID, _ := newWorkspaceFixture(t)
	addTask(t, db, iniID, "add /health")
	addTask(t, db, iniID, "add /metrics")
	ctx := context.Background()
	first, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	// Task 1 is not done until its change set is applied, so make task 2 the
	// next ready one the way a user skipping ahead would.
	if err := db.SetTaskStatus(ctx, first.TaskID, domain.TaskDone); err != nil {
		t.Fatal(err)
	}
	_, err = eng.Execute(ctx, iniID)
	if err == nil {
		t.Fatal("task 2 ran while task 1's change set was still awaiting review")
	}
	if !strings.Contains(err.Error(), first.HumanID) {
		t.Errorf("the refusal does not name the open change set: %v", err)
	}
}

// Each attempt starts from the workspace, not from what the last attempt left.
func TestEachAttemptStartsFromTheWorkspace(t *testing.T) {
	eng, db, iniID, _ := newWorkspaceFixture(t)
	addTask(t, db, iniID, "add /health")
	ctx := context.Background()
	root := filepath.Join(eng.workRoot, "work", iniID)
	writeFile(t, filepath.Join(root, "leftover.txt"), "from an earlier attempt\n")
	if _, err := eng.Execute(ctx, iniID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "leftover.txt")); err == nil {
		t.Error("an earlier attempt's file survived into the next attempt")
	}
}

// The review diff shows the change as hunks against the file the agent
// started from.
func TestReviewDiffIsAHunkAgainstTheBase(t *testing.T) {
	eng, db, iniID, ws := newWorkspaceFixture(t)
	addTask(t, db, iniID, "add /health")
	cs, err := eng.Execute(context.Background(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	var sf StructuredFile
	for _, f := range eng.StructuredDiff(&cs) {
		if f.Path == "server.js" {
			sf = f
		}
	}
	want := "@@ -1,1 +1,2 @@\n app.get('/', hello)\n+app.get('/health', ok)"
	if sf.Patch != want {
		t.Errorf("patch:\n%s\nwant:\n%s", sf.Patch, want)
	}
	// Edited after the agent ran: the workspace file is no longer the base,
	// and diffing against it would show the user's edit, reversed, as the
	// agent's. Say so instead.
	writeFile(t, filepath.Join(ws, "server.js"), "app.get('/', hello)\napp.get('/users', list)\n")
	for _, f := range eng.StructuredDiff(&cs) {
		if f.Path == "server.js" {
			if f.Patch != "" || f.PatchNote == "" {
				t.Errorf("diffed against a file edited after the run: patch=%q note=%q", f.Patch, f.PatchNote)
			}
		}
	}
}

// A change set records the executor that made it, so the UI labels it by what
// produced it, not by whatever executor is selected today.
func TestChangeSetRecordsItsExecutor(t *testing.T) {
	eng, db, iniID, _ := newWorkspaceFixture(t)
	addTask(t, db, iniID, "add /health")
	cs, err := eng.Execute(context.Background(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.GetChangeSet(context.Background(), cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Executor != "hermes" {
		t.Errorf("executor = %q, want hermes", got.Executor)
	}
}

func TestPatchNotesForEdgeCases(t *testing.T) {
	eng, _, _ := newExecuteFixture(t)
	put := func(s string) string {
		d, err := eng.contentBlobs().Put([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	base := put("a\nb\n")
	// A file emptied by the change is a diff of deletions, not "not kept".
	empty := put("")
	p, note := eng.patchFor(domain.FileChange{Path: "f", Digest: empty, ChangeType: domain.FileModified, BaseDigest: base},
		StructuredFile{contentKept: true})
	if p != "@@ -1,2 +0,0 @@\n-a\n-b" || note != "" {
		t.Errorf("emptied file: patch=%q note=%q", p, note)
	}
	// Only line endings changed: no hunk, so say so.
	crlf := put("a\r\nb\r\n")
	_, note = eng.patchFor(domain.FileChange{Path: "f", Digest: crlf, ChangeType: domain.FileModified, BaseDigest: base},
		StructuredFile{Content: "a\r\nb\r\n", Size: 6, contentKept: true})
	if !strings.Contains(note, "line endings") {
		t.Errorf("a line-ending-only change gave note %q", note)
	}
}
