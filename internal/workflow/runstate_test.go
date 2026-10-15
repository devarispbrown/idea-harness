package workflow

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/security"
)

// scriptedAgentDocker is a fake docker whose agent writes $FAKE_WRITE (with
// $FAKE_CONTENT) into the work root, prints a couple of lines, and keeps
// "running" until the file $FAKE_HOLD exists.
const scriptedAgentDocker = `#!/bin/sh
case "$1" in
  run)
    [ -n "$FAKE_RUN_FAIL" ] && exit 125
    prev=""
    for a in "$@"; do
      if [ "$prev" = "-v" ] && [ "${a%:/work}" != "$a" ]; then work="${a%:/work}"; fi
      prev="$a"
    done
    if [ -n "$FAKE_WRITE" ]; then mkdir -p "$(dirname "$work/$FAKE_WRITE")"; printf '%s' "$FAKE_CONTENT" > "$work/$FAKE_WRITE"; fi
    echo "cid-scripted" ;;
  inspect)
    if [ -n "$FAKE_HOLD" ] && [ ! -f "$FAKE_HOLD" ]; then echo "running 0"; else echo "exited 0"; fi ;;
  logs) printf 'planning the change\nusing key sk-test-leaked-value-123456\n' ;;
  rm) exit 0 ;;
  *) exit 1 ;;
esac
`

func newScriptedFixture(t *testing.T) (*Engine, string, string) {
	t.Helper()
	eng, db, iniID, ws := newWorkspaceFixture(t) // hermes, a git workspace with server.js
	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "docker"), scriptedAgentDocker)
	if err := os.Chmod(filepath.Join(bin, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	addTask(t, db, iniID, "add /health")
	return eng, iniID, ws
}

// stopMidRun runs Execute with an agent that writes path and never finishes,
// and cancels it, the way Stop does.
func stopMidRun(t *testing.T, eng *Engine, iniID, path, content string) {
	t.Helper()
	t.Setenv("FAKE_WRITE", path)
	t.Setenv("FAKE_CONTENT", content)
	t.Setenv("FAKE_HOLD", filepath.Join(t.TempDir(), "never"))
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel once the agent has written its file, not after a fixed delay:
	// under -race on a loaded machine the planner can outlast 300 ms, and a
	// cancel before the agent ran kept nothing -- a flake, not a defect.
	written := filepath.Join(eng.workRoot, "work", iniID, path)
	go func() {
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if path == "" {
				// Nothing to wait for on disk, but still wait for the agent
				// itself to be running: a fixed delay raced the planner on a
				// loaded machine, and cancelling before Start returned took a
				// different path through Execute than the test is about.
				if state, _ := eng.AgentOutput(context.Background(), iniID); state == LiveRunning {
					break
				}
				time.Sleep(20 * time.Millisecond)
				continue
			}
			// The agent has started (Start returned; cancelling earlier kills
			// `docker run` itself, and a run that never started keeps
			// nothing) AND written its content -- content, not existence,
			// since a modified file is already in the seeded work root.
			state, _ := eng.AgentOutput(context.Background(), iniID)
			if got, err := os.ReadFile(written); state == LiveRunning && err == nil && string(got) == content {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
	}()
	if _, err := eng.Execute(ctx, iniID); err == nil {
		t.Fatal("a stopped run produced a change set")
	}
}

// finishRun runs Execute with an agent that writes path and finishes.
func finishRun(t *testing.T, eng *Engine, iniID, path, content string) domain.ChangeSet {
	t.Helper()
	t.Setenv("FAKE_WRITE", path)
	t.Setenv("FAKE_CONTENT", content)
	t.Setenv("FAKE_HOLD", "")
	cs, err := eng.Execute(context.Background(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func changeSetPaths(t *testing.T, eng *Engine, cs domain.ChangeSet) map[string]bool {
	t.Helper()
	patch, err := eng.loadPatch(&cs)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, f := range patch.Files {
		out[f.Path] = true
	}
	return out
}

func findingOf(t *testing.T, eng *Engine, cs domain.ChangeSet, category string) *domain.Finding {
	t.Helper()
	fs, err := eng.db.ListFindingsByTarget(context.Background(), "change_set", cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range fs {
		if fs[i].Category == category {
			return &fs[i]
		}
	}
	return nil
}

// Stopping a run used to throw away everything the agent wrote: each run
// starts from a fresh copy of the workspace. Run again now continues from it.
func TestRunAgainContinuesFromAStoppedRun(t *testing.T) {
	eng, iniID, _ := newScriptedFixture(t)
	stopMidRun(t, eng, iniID, "a.txt", "from the stopped run\n")
	if pw := eng.PartialWork(iniID); pw == nil || pw.Files != 1 {
		t.Fatalf("the stopped run's file was not kept: %+v", pw)
	}

	cs := finishRun(t, eng, iniID, "b.txt", "from the second run\n")
	paths := changeSetPaths(t, eng, cs)
	if !paths["a.txt"] || !paths["b.txt"] {
		t.Errorf("the change set has %v, want the stopped run's a.txt and the new b.txt", paths)
	}
	f := findingOf(t, eng, cs, "carried_over")
	if f == nil || !strings.Contains(f.Evidence, "a.txt") {
		t.Errorf("nothing on the change set says a.txt was carried: %+v", f)
	}
	if eng.PartialWork(iniID) != nil {
		t.Error("the kept files were not cleared once carried")
	}
}

// TestStopBeforeAnyWriteIsLoggedNotSilent reproduces the ICP re-run's own
// defect F: a real Stop landed while the agent had only been reading files
// (no write yet), so capturePartial's diff was genuinely empty -- correct
// behavior -- but its early return logged NOTHING at all, on either the
// success or the "nothing to keep" path. An operator watching daemon.log
// after a Stop could not tell "capturePartial ran and found nothing to keep"
// apart from "capturePartial never ran" or "it silently swallowed an error",
// which is exactly the "Stop is unverifiable" complaint. capturePartial must
// now say what it decided even when it keeps nothing.
func TestStopBeforeAnyWriteIsLoggedNotSilent(t *testing.T) {
	eng, iniID, _ := newScriptedFixture(t)
	var logBuf bytes.Buffer
	eng.log = slog.New(slog.NewTextHandler(&logBuf, nil))

	// path == "" tells stopMidRun to cancel shortly after the agent starts,
	// without ever writing FAKE_WRITE -- the "nothing written yet" case.
	stopMidRun(t, eng, iniID, "", "")

	if pw := eng.PartialWork(iniID); pw != nil {
		t.Fatalf("PartialWork = %+v, want nil: nothing was ever written", pw)
	}
	log := logBuf.String()
	if !strings.Contains(log, "checking for files to keep") {
		t.Errorf("daemon log = %q, want a line confirming capturePartial ran", log)
	}
	if !strings.Contains(log, "nothing to keep") {
		t.Errorf("daemon log = %q, want a line saying capturePartial found nothing to keep", log)
	}
}

// A file that appeared in the workspace at the carried path since is not
// overwritten: the carried add is skipped, and the finding says so.
func TestAStaleCarriedFileIsSkipped(t *testing.T) {
	eng, iniID, ws := newScriptedFixture(t)
	stopMidRun(t, eng, iniID, "a.txt", "from the stopped run\n")
	writeFile(t, filepath.Join(ws, "a.txt"), "written by hand since\n")

	cs := finishRun(t, eng, iniID, "b.txt", "x\n")
	f := findingOf(t, eng, cs, "carried_over")
	if f == nil || !strings.Contains(f.Evidence, "not carried") || !strings.Contains(f.Evidence, "a.txt") {
		t.Errorf("the skipped file is not reported: %+v", f)
	}
	root := filepath.Join(eng.workRoot, "work", iniID)
	if got, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(got) != "written by hand since\n" {
		t.Errorf("the carried file overwrote the workspace's: %q", got)
	}
}

// Kept files belong to one task. The next run of a different task discards
// them instead of carrying them into the wrong task.
func TestKeptFilesAreNotCarriedIntoAnotherTask(t *testing.T) {
	eng, iniID, _ := newScriptedFixture(t)
	stopMidRun(t, eng, iniID, "a.txt", "x\n")
	tasks, err := eng.db.ListTasks(context.Background(), iniID)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.db.SetTaskStatus(context.Background(), tasks[0].ID, domain.TaskCancelled); err != nil {
		t.Fatal(err)
	}
	addTask(t, eng.db, iniID, "another task")

	cs := finishRun(t, eng, iniID, "b.txt", "y\n")
	if changeSetPaths(t, eng, cs)["a.txt"] {
		t.Error("a file kept for one task was carried into another")
	}
}

// Content that looks like a secret is never written to the redaction-free
// store, so it is not kept -- and the reason is visible.
func TestASecretIsNotKept(t *testing.T) {
	eng, iniID, _ := newScriptedFixture(t)
	stopMidRun(t, eng, iniID, "config.py", "AWS_SECRET = 'AKIAIOSFODNN7EXAMPLE'\n")
	pw := eng.PartialWork(iniID)
	if pw == nil || pw.Files != 0 || pw.Note == "" {
		t.Errorf("a secret-bearing file was kept, or silently dropped: %+v", pw)
	}
}

// The kept files are a file on disk, not engine memory, so they survive a
// daemon restart; and a torn or corrupt file is ignored, never fatal.
func TestKeptFilesSurviveARestartAndCorruptionIsIgnored(t *testing.T) {
	eng, iniID, _ := newScriptedFixture(t)
	stopMidRun(t, eng, iniID, "a.txt", "x\n")
	restarted := &Engine{db: eng.db, blobs: eng.blobs, rawBlobs: eng.rawBlobs, registry: eng.registry,
		cfg: eng.cfg, log: eng.log, workRoot: eng.workRoot, workspaceRoot: eng.workspaceRoot}
	if restarted.PartialWork(iniID) == nil {
		t.Fatal("the kept files did not survive a restart")
	}
	if err := os.WriteFile(restarted.partialPath(iniID), []byte("{torn"), 0o644); err != nil {
		t.Fatal(err)
	}
	if restarted.PartialWork(iniID) != nil {
		t.Error("a corrupt record was read as kept files")
	}
}

func TestStartOverDiscards(t *testing.T) {
	eng, iniID, _ := newScriptedFixture(t)
	stopMidRun(t, eng, iniID, "a.txt", "x\n")
	if err := eng.DiscardPartial(iniID); err != nil {
		t.Fatal(err)
	}
	cs := finishRun(t, eng, iniID, "b.txt", "y\n")
	if changeSetPaths(t, eng, cs)["a.txt"] {
		t.Error("Start over still carried the stopped run's file")
	}
}

// The running agent's output is readable while Execute holds the engine
// lock, is redacted, and goes away when the run ends.
func TestLiveAgentOutputDuringARun(t *testing.T) {
	eng, iniID, _ := newScriptedFixture(t)
	eng.SetRedactor(security.NewRedactor("sk-test-leaked-value-123456"))
	hold := filepath.Join(t.TempDir(), "release")
	t.Setenv("FAKE_WRITE", "b.txt")
	t.Setenv("FAKE_CONTENT", "y\n")
	t.Setenv("FAKE_HOLD", hold)

	done := make(chan error, 1)
	go func() { _, err := eng.Execute(context.Background(), iniID); done <- err }()

	deadline := time.Now().Add(10 * time.Second)
	var state string
	var out string
	for time.Now().Before(deadline) {
		got := make(chan struct{})
		go func() { state, out = eng.AgentOutput(context.Background(), iniID); close(got) }()
		select {
		case <-got:
		case <-time.After(3 * time.Second):
			t.Fatal("reading the agent's output blocked on the running Execute")
		}
		if state == LiveRunning && out != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if state != LiveRunning || !strings.Contains(out, "planning the change") {
		t.Fatalf("no live output while running: state=%v out=%q", state, out)
	}
	if strings.Contains(out, "sk-test-leaked-value-123456") {
		t.Errorf("the key reached the live output: %q", out)
	}

	writeFile(t, hold, "")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if state, out := eng.AgentOutput(context.Background(), iniID); state != LiveIdle || out != "" {
		t.Errorf("output still reported after the run: state=%v out=%q", state, out)
	}
}

// A Continue that fails before producing a change set -- Docker down, an image
// pull failing, Stop during start -- must not lose the kept files.
func TestAContinueThatFailsToStartKeepsTheFiles(t *testing.T) {
	eng, iniID, _ := newScriptedFixture(t)
	stopMidRun(t, eng, iniID, "a.txt", "x\n")
	t.Setenv("FAKE_RUN_FAIL", "1")
	if _, err := eng.Execute(context.Background(), iniID); err == nil {
		t.Fatal("a run whose container never started succeeded")
	}
	t.Setenv("FAKE_RUN_FAIL", "")
	if pw := eng.PartialWork(iniID); pw == nil || pw.Files != 1 {
		t.Fatalf("the kept files were lost when the Continue failed to start: %+v", pw)
	}
	cs := finishRun(t, eng, iniID, "b.txt", "y\n")
	if !changeSetPaths(t, eng, cs)["a.txt"] {
		t.Error("the next run did not carry the kept file")
	}
}

// A Continue in which the agent does nothing still says so: the carried files
// alone must not read as a finished task.
func TestACarriedOnlyRunStillSaysTheAgentDidNothing(t *testing.T) {
	eng, iniID, _ := newScriptedFixture(t)
	stopMidRun(t, eng, iniID, "a.txt", "x\n")
	cs := finishRun(t, eng, iniID, "", "")
	if !changeSetPaths(t, eng, cs)["a.txt"] {
		t.Fatal("the carried file is missing")
	}
	if findingOf(t, eng, cs, "agent_output") == nil {
		t.Error("an agent that changed nothing was hidden behind the carried files")
	}
}

// A modified file is carried too, where the workspace still matches.
func TestAModifiedFileIsCarried(t *testing.T) {
	eng, iniID, _ := newScriptedFixture(t)
	stopMidRun(t, eng, iniID, "server.js", "app.get('/', hello)\napp.get('/half', done)\n")
	cs := finishRun(t, eng, iniID, "b.txt", "y\n")
	f := fileChange(t, eng, cs, "server.js")
	if f.ChangeType != domain.FileModified {
		t.Errorf("server.js carried as %q, want modified", f.ChangeType)
	}
}

// Once the agent's container is gone the view says finished, with its final
// output -- not "running" with an empty log through the build checks.
func TestLiveStateAfterTheAgentFinishes(t *testing.T) {
	eng, _, _ := newScriptedFixture(t)
	eng.setLiveStarting("ini")
	if st, _ := eng.AgentOutput(context.Background(), "ini"); st != LiveStarting {
		t.Errorf("before the agent starts: %s, want starting", st)
	}
	eng.markLiveDone("ini", "last line from the agent")
	st, out := eng.AgentOutput(context.Background(), "ini")
	if st != LiveFinished || out != "last line from the agent" {
		t.Errorf("after the agent: %s %q, want finished with its final output", st, out)
	}
}

// Reopen and revise discard kept files; so, in the daemon, does editing the
// task's wording. Here: the engine side of that contract.
func TestDiscardIsIdempotent(t *testing.T) {
	eng, iniID, _ := newScriptedFixture(t)
	if err := eng.DiscardPartial(iniID); err != nil {
		t.Errorf("discarding with nothing kept failed: %v", err)
	}
}

// What the agent printed stays readable after the run: the running card that
// showed it live goes away when the run finishes.
func TestTheChangeSetKeepsTheAgentsOutput(t *testing.T) {
	eng, iniID, _ := newScriptedFixture(t)
	eng.SetRedactor(security.NewRedactor("sk-test-leaked-value-123456"))
	cs := finishRun(t, eng, iniID, "b.txt", "y\n")
	got, err := eng.db.GetChangeSet(context.Background(), cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.AgentOutput, "planning the change") {
		t.Errorf("the change set does not keep the agent's output: %q", got.AgentOutput)
	}
	if strings.Contains(got.AgentOutput, "sk-test-leaked-value-123456") {
		t.Errorf("the stored output carries the key: %q", got.AgentOutput)
	}
}
