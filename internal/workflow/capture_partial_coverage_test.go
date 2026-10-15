package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// rmFailsAgentDocker is editingAgentDocker's shape (writes to server.js, the
// way an agent asked to add a route does), but its cleanup ("docker rm",
// which Collect calls) fails -- deterministically, no timing/races needed --
// reproducing "the agent finished, and something on the way to a change set
// failed" without needing to catch a Stop mid-flight.
const rmFailsAgentDocker = `#!/bin/sh
case "$1" in
  run)
    prev=""
    for a in "$@"; do
      if [ "$prev" = "-v" ] && [ "${a%:/work}" != "$a" ]; then work="${a%:/work}"; fi
      prev="$a"
    done
    echo "app.get('/health', ok)" >> "$work/server.js"
    echo "cid-rmfail" ;;
  inspect) echo "exited 0" ;;
  logs) echo "added /health" ;;
  rm) exit 1 ;;
  *) exit 1 ;;
esac
`

// TestExecuteKeepsFilesWhenCollectFailsAfterAgentFinished is the MEDIUM
// "capturePartial runs on only one cancel branch" fix: before it,
// capturePartial ran ONLY when waitForCompletion itself failed (a Stop while
// the agent was still running/being cancelled). Once the agent has actually
// finished -- waitForCompletion succeeded -- anything failing on the way to
// CreateChangeSet (here: Collect's own cleanup) returned with the agent's
// written files never captured, and the next Execute's seedWorkRoot
// unconditionally removes the work root, losing them for good.
func TestExecuteKeepsFilesWhenCollectFailsAfterAgentFinished(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(rmFailsAgentDocker), 0o755); err != nil {
		t.Fatal(err)
	}
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
	task := addTask(t, db, iniID, "add /health")

	_, err := eng.Execute(context.Background(), iniID)
	if err == nil {
		t.Fatal("expected Execute to fail (docker rm fails), even though the agent itself finished")
	}

	info := eng.PartialWork(iniID)
	if info == nil {
		t.Fatal("MEDIUM: capturePartial did not run for a failure between the agent finishing and CreateChangeSet -- " +
			"the agent's file is lost, and the next seedWorkRoot removes the work root")
	}
	if info.TaskID != task.ID {
		t.Errorf("kept partial belongs to task %q, want %q", info.TaskID, task.ID)
	}
	if info.Files != 1 {
		t.Fatalf("kept partial reports %d files, want 1 (server.js): %+v", info.Files, info)
	}
}
