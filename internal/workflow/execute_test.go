package workflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/idea-harness/idea-harness/internal/config"
	"github.com/idea-harness/idea-harness/internal/domain"
	"github.com/idea-harness/idea-harness/internal/model"
	"github.com/idea-harness/idea-harness/internal/store"
)

func newExecuteFixture(t *testing.T) (*Engine, *store.DB, string) {
	t.Helper()
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	// Isolate both the executor work root and the canonical workspace per test.
	eng.SetWorkRoot(t.TempDir())
	eng.SetWorkspaceRoot(t.TempDir())
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	return eng, db, ini.ID
}

func TestExecuteCreatesReviewRequiredChangeSet(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}

	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.HumanID != "CHG-1" {
		t.Fatalf("expected CHG-1, got %q", cs.HumanID)
	}
	if cs.TaskID != task.ID {
		t.Fatalf("expected task id %q, got %q", task.ID, cs.TaskID)
	}
	if cs.PatchDigest == "" {
		t.Fatal("expected a patch digest")
	}
	if cs.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("expected review_required, got %q", cs.Status)
	}

	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("expected persisted review_required, got %q", got.Status)
	}
}

func TestExecuteRejectsOnSecretMarker(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Store password in vault", Statement: "secret", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}

	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetRejected {
		t.Fatalf("expected rejected (secret marker), got %q", cs.Status)
	}

	// The rejection must be traceable via a blocking secret_sweep finding.
	findings, err := db.ListFindingsByInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	var blocked bool
	for _, f := range findings {
		if f.TargetID == cs.ID && f.Category == "secret_sweep" && f.Severity == domain.SeverityCritical {
			blocked = true
		}
	}
	if !blocked {
		t.Fatal("expected a critical secret_sweep finding explaining the rejection")
	}
}

func TestExecuteRecordsDeterministicCheckFindings(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("expected review_required, got %q", cs.Status)
	}

	findings, err := db.ListFindingsByInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	// B5: patch-applies now runs for real (pass), while build/tests are
	// not_applicable because no build/test command is configured.
	got := map[string]domain.Finding{}
	for _, f := range findings {
		if f.TargetID == cs.ID {
			got[f.Category] = f
		}
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 deterministic-check findings, got %d: %+v", len(got), got)
	}
	if got["patch_applies"].Severity.Blocking() {
		t.Errorf("patch_applies should pass, got %+v", got["patch_applies"])
	}
	for _, c := range []string{"build", "tests"} {
		if got[c].Disposition != domain.DispositionNotApplicable {
			t.Errorf("%s should be not_applicable (no command), got %+v", c, got[c])
		}
	}
}

func TestApplyRequiresApproval(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}

	if err := eng.ApplyChangeSet(ctx, iniID, cs.ID); err == nil {
		t.Fatal("expected apply of a non-approved change set to fail")
	}
}

func TestReviewApproveThenApply(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}

	// The demo reviewer emits no findings, so approval is not blocked.
	if err := eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetApproved {
		t.Fatalf("expected approved, got %q", got.Status)
	}

	if err := eng.ApplyChangeSet(ctx, iniID, cs.ID); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetApplied {
		t.Fatalf("expected applied, got %q", got.Status)
	}

	journal, err := db.ListJournal(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	// H2: prepared → workspace_mutated → committed.
	if len(journal) != 3 || journal[2].State != domain.ApplyCommitted {
		t.Fatalf("expected prepared, workspace_mutated, committed journal rows, got %+v", journal)
	}

	// The real file must have been written into the canonical workspace.
	data, err := os.ReadFile(filepath.Join(eng.workspaceRoot, "hello.txt"))
	if err != nil {
		t.Fatalf("read applied hello.txt: %v", err)
	}
	if string(data) != "task: Implement core\n" {
		t.Fatalf("hello.txt = %q, want %q", string(data), "task: Implement core\n")
	}
}

func TestRevertAppendsRolledBackJournal(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if err := eng.ApplyChangeSet(ctx, iniID, cs.ID); err != nil {
		t.Fatal(err)
	}

	if err := eng.RevertChangeSet(ctx, iniID, cs.ID); err != nil {
		t.Fatal(err)
	}
	journal, err := db.ListJournal(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	// H2: prepared → workspace_mutated → committed → rolled_back.
	if len(journal) != 4 || journal[3].State != domain.ApplyRolledBack {
		t.Fatalf("expected prepared, workspace_mutated, committed, rolled_back journal rows, got %+v", journal)
	}

	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetReverted {
		t.Fatalf("expected reverted status after rollback, got %q", got.Status)
	}

	// The applied file must have been removed on revert (it did not exist before).
	if _, err := os.Stat(filepath.Join(eng.workspaceRoot, "hello.txt")); !os.IsNotExist(err) {
		t.Fatalf("hello.txt should be removed after revert, stat err = %v", err)
	}
}

func TestSelectExecutor(t *testing.T) {
	cases := []struct {
		kind config.ExecutorKind
		want string
	}{
		{config.ExecutorInternal, "internal"},
		{config.ExecutorContainer, "container"},
		{config.ExecutorOMP, "omp"},
		{config.ExecutorHermes, "hermes"},
		{config.ExecutorKind(""), "internal"},  // empty/unknown fails closed to internal
		{config.ExecutorKind("bogus"), "internal"},
	}
	for _, tc := range cases {
		e := &Engine{cfg: config.HarnessConfig{Executor: config.ExecutorConfig{Kind: tc.kind}}}
		exec := e.selectExecutor()
		if exec == nil {
			t.Fatalf("selectExecutor(%q) returned nil", tc.kind)
		}
		if got := exec.Name(); got != tc.want {
			t.Errorf("selectExecutor(%q).Name() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func dockerAvailable() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	return exec.Command("docker", "info").Run() == nil
}

func TestExecuteRejectsOnFailingBuild(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available")
	}
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{
		Network:  config.NetworkDisabled,
		Review:   config.ReviewConfig{MaxCycles: 3},
		Check:    config.CheckConfig{Image: "alpine:3.20"},
		BuildCmd: "exit 1",
	})
	eng.SetWorkRoot(t.TempDir())
	eng.SetWorkspaceRoot(t.TempDir())
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetRejected {
		t.Fatalf("expected rejected (failing build command), got %q", cs.Status)
	}
}

func TestExecutePassesConfiguredBuild(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available")
	}
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{
		Network:  config.NetworkDisabled,
		Review:   config.ReviewConfig{MaxCycles: 3},
		Check:    config.CheckConfig{Image: "alpine:3.20"},
		BuildCmd: "echo ok",
		TestCmd:  "echo ok",
	})
	eng.SetWorkRoot(t.TempDir())
	eng.SetWorkspaceRoot(t.TempDir())
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("expected review_required (build passes), got %q", cs.Status)
	}
}

func TestChangesRequestedReopensTaskAndSupersedes(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}

	cs1, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs1.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("expected review_required, got %q", cs1.Status)
	}

	// Human requests changes: the task must re-open so a re-execute can supersede.
	if err := eng.ReviewChangeSet(ctx, iniID, cs1.ID, "changes_requested"); err != nil {
		t.Fatal(err)
	}
	tasks, err := db.ListTasks(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != domain.TaskProposed {
		t.Fatalf("expected task re-opened to proposed, got %+v", tasks)
	}

	cs2, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs2.ID == cs1.ID {
		t.Fatal("expected a new change set")
	}
	got1, err := db.GetChangeSet(ctx, cs1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got1.Status != domain.ChangeSetSuperseded {
		t.Fatalf("expected prior change set superseded, got %q", got1.Status)
	}
	if cs2.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("expected new change set review_required, got %q", cs2.Status)
	}
}

func TestExecuteRejectsBuildWhenNoSandboxImage(t *testing.T) {
	// Deterministic fail-closed path: BuildCmd set but no check.image → the check
	// refuses to run on the host and rejects the change set (no Docker needed).
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{
		Network:  config.NetworkDisabled,
		Review:   config.ReviewConfig{MaxCycles: 3},
		BuildCmd: "echo ok",
	})
	eng.SetWorkRoot(t.TempDir())
	eng.SetWorkspaceRoot(t.TempDir())
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetRejected {
		t.Fatalf("expected rejected (no sandbox image), got %q", cs.Status)
	}
}

func TestTaskDependenciesApplied(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	a := &domain.Task{InitiativeID: iniID, Title: "A", Statement: "a", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, a, "", nil); err != nil {
		t.Fatal(err)
	}
	b := &domain.Task{InitiativeID: iniID, Title: "B", Statement: "b", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, b, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.AddTaskDependencies(ctx, iniID, b.ID, []string{a.ID}); err != nil {
		t.Fatal(err)
	}

	// A has no deps -> ready.
	readyA, err := eng.taskDependenciesApplied(ctx, iniID, a.ID)
	if err != nil || !readyA {
		t.Fatalf("A should be ready, got ready=%v err=%v", readyA, err)
	}
	// B depends on A, which has no applied change set yet -> not ready.
	readyB, err := eng.taskDependenciesApplied(ctx, iniID, b.ID)
	if err != nil || readyB {
		t.Fatalf("B should be blocked (A not applied), got ready=%v err=%v", readyB, err)
	}

	// Apply A's change set (force status via SQL for the test).
	cs := &domain.ChangeSet{InitiativeID: iniID, TaskID: a.ID, PatchDigest: "patch"}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`UPDATE change_sets SET status = 'applied' WHERE id = ?`, cs.ID); err != nil {
		t.Fatal(err)
	}

	readyB, err = eng.taskDependenciesApplied(ctx, iniID, b.ID)
	if err != nil || !readyB {
		t.Fatalf("B should now be ready (A applied), got ready=%v err=%v", readyB, err)
	}
}
