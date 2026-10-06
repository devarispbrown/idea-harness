package cli

import (
	"fmt"
	"io"
	"net/url"
	"strings"
)

// ---------- plan editing: add/merge/split/depends-on/reorder ----------
//
// The API and the web UI could already add a task, merge two, split one, set a
// task's depends_on, and reorder the plan (internal/daemon/handlers.go, "task
// mutations (Plan editing)"); the CLI had no verb for any of the five, and
// internal/cli/advance.go's cliTask DTO even carried a comment naming the gap
// by number (ICP re-run 5). An evaluator editing a plan from the CLI had to
// drop to raw HTTP twice. Follows judgment.go's shape: a thin command that
// resolves references, validates what the server would 400 on anyway, and
// prints a short confirmation.
//
// Reference resolution, split across two layers on purpose:
//   - resolveTaskRefs (handlers.go) already accepts an id, a code, or a title
//     for the MEMBERS of a depends_on list, and is left alone here.
//   - every other place a task is named -- the target of merge/split/
//     depends-on/reorder -- is a raw id in the URL path or the request body on
//     every one of those five endpoints (handleMergeTasks/handleSplitTask/
//     handleSetTaskDependsOn/handleReorderTasks all call pr.db.GetTask or
//     compare ids directly, never resolveTaskRefs). fetchTasks/findTask below
//     do client-side what the server does not: accept an id, a code, or a
//     title there too, by fetching the list once and matching against it --
//     the same thing resolveObjectID already does for `idea why task …`.

// fetchTasks lists an initiative's tasks once, for the plan-editing verbs that
// need to resolve one or more references against the current set (and, for
// `reorder`, need the full count to validate the list is complete before
// sending it).
func fetchTasks(c *client, projectID, iniID string) ([]cliTask, error) {
	var tasks []cliTask
	if err := c.do("GET", initiativeResource(projectID, iniID, "tasks"), nil, &tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

// findTask resolves ref -- an id, a code, or a title -- against an
// already-fetched task list. Exact id first, so an id never runs through the
// title/code comparison (mirroring resolveTaskRefs's own ordering, for the
// same reason: a task whose id collides with another task's title or code is
// astronomically unlikely, but id match is unambiguous regardless).
func findTask(tasks []cliTask, ref string) (*cliTask, error) {
	for i := range tasks {
		if tasks[i].ID == ref {
			return &tasks[i], nil
		}
	}
	for i := range tasks {
		if tasks[i].Code == ref || tasks[i].Title == ref {
			return &tasks[i], nil
		}
	}
	return nil, fmt.Errorf("no task matching %q; `idea tasks` lists them", ref)
}

// codesFor maps task ids back to their codes for display, falling back to the
// raw id when it is not in the given list (e.g. a task from before this
// command's own fetchTasks call).
func codesFor(tasks []cliTask, ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id
		for _, t := range tasks {
			if t.ID == id {
				out[i] = t.Code
				break
			}
		}
	}
	return out
}

// splitCSV splits a comma-separated reference list, trimming whitespace and
// dropping empty entries -- so `--requirements "REQ-1, REQ-2"` and
// `--requirements REQ-1,REQ-2` both work, and a trailing comma does not
// produce a phantom empty requirement reference.
func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// taskPriorities are the valid task priority values the daemon accepts
// (domain.RequirementPriority). Mirrored here, rather than importing
// internal/domain, the same way judgment.go's reopenPhases mirrors the
// daemon's accepted design phases: refusing a typo before the round trip
// needs the list, not the type.
var taskPriorities = []string{"must", "should", "could"}

// cmdAddTask adds a new plan task. A task added after the plan is approved
// must name at least one requirement -- the server enforces this
// synchronously (handleAddTask, review A4 #3/#4) because there is no
// plan-review cycle left to catch an orphaned task -- so this is refused here
// before the round trip, with the same reasoning the server's own comment
// gives, rather than relayed as the server's 400.
func cmdAddTask(a cmdArgs, stdout, stderr io.Writer) error {
	if len(a.positional) == 0 || strings.TrimSpace(a.positional[0]) == "" {
		return fmt.Errorf(`usage: idea add-task "<title>" --requirement <ref> [--requirement <ref>...] ` +
			`[--statement <text>] [--priority must|should|could] [--estimate <text>] ` +
			`[--depends-on <ref>]... [<initiative>]`)
	}
	title := a.positional[0]
	reqs := a.list("--requirement")
	if len(reqs) == 0 {
		return fmt.Errorf("add-task needs at least one --requirement " +
			"(a task the plan cannot trace to a requirement is refused); e.g. --requirement REQ-3")
	}
	priority := a.value("--priority")
	if priority != "" && !contains(taskPriorities, priority) {
		return fmt.Errorf("invalid --priority %q; use one of: %s", priority, strings.Join(taskPriorities, ", "))
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, a.positional[1:])
	if err != nil {
		return err
	}
	body := map[string]any{
		"title":           title,
		"statement":       a.value("--statement"),
		"priority":        priority,
		"estimate":        a.value("--estimate"),
		"requirement_ids": reqs,
	}
	if deps := a.list("--depends-on"); len(deps) > 0 {
		body["depends_on"] = deps
	}
	var task cliTask
	if err := c.do("POST", initiativeResource(projectID, iniID, "tasks"), body, &task); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ added")+" "+code(task.Code)+" "+bold(task.Title))
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// cmdMergeTasks merges two tasks into one, keeping the first-named. This
// deletes the second task (its requirements, statement and dependencies move
// onto the survivor first -- handleMergeTasks/MergeTasks), so it is refused
// without --yes, matching how `idea executors setup` gates a tradeoff it
// cannot undo either.
func cmdMergeTasks(a cmdArgs, stdout, stderr io.Writer) error {
	if len(a.positional) < 2 {
		return fmt.Errorf("usage: idea merge-tasks <task1> <task2> [<initiative>] [--yes]")
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, a.positional[2:])
	if err != nil {
		return err
	}
	tasks, err := fetchTasks(c, projectID, iniID)
	if err != nil {
		return err
	}
	keep, err := findTask(tasks, a.positional[0])
	if err != nil {
		return err
	}
	merge, err := findTask(tasks, a.positional[1])
	if err != nil {
		return err
	}
	if keep.ID == merge.ID {
		return fmt.Errorf("merge-tasks needs two distinct tasks; %q and %q both name %s",
			a.positional[0], a.positional[1], keep.Code)
	}
	fmt.Fprintln(stderr, warn("⚠ ")+code(merge.Code)+warn(" (\""+merge.Title+"\") will be deleted; its requirements, "+
		"statement and dependencies move onto ")+code(keep.Code)+warn(" (\""+keep.Title+"\"). This cannot be undone from the CLI."))
	if !a.has("--yes") {
		return fmt.Errorf("pass --yes to confirm merging %s into %s", merge.Code, keep.Code)
	}
	var out cliTask
	if err := c.do("POST", initiativeResource(projectID, iniID, "tasks/merge"),
		map[string]any{"task_ids": []string{keep.ID, merge.ID}}, &out); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ merged")+" "+code(merge.Code)+" "+muted("into")+" "+code(out.Code))
	return nil
}

// splitPart is one replacement task in `idea split-task`'s request body,
// mirroring handleSplitTask's `tasks[]` entries.
type splitPart struct {
	Title          string   `json:"title"`
	Statement      string   `json:"statement,omitempty"`
	RequirementIDs []string `json:"requirement_ids"`
}

// cmdSplitTask splits one task into two or more. Each replacement is named by
// a `--title`/`--requirements` pair (an optional `--statement` alongside them),
// matched up by the order each flag was given -- the nth --title pairs with
// the nth --requirements and, if present, the nth --statement. That shape was
// chosen over one delimited string per replacement (e.g. "title::req1,req2")
// because it needs no escaping convention for a title that happens to contain
// the delimiter, and over indexed flag names (`--title-1`, `--requirements-1`)
// because those are not something a person reaches for from memory.
//
// Deletes the original (its dependents and outgoing dependencies carry over to
// every replacement -- handleSplitTask's own doc comment), so it is refused
// without --yes, the same gate `merge-tasks` uses.
func cmdSplitTask(a cmdArgs, stdout, stderr io.Writer) error {
	if len(a.positional) < 1 {
		return fmt.Errorf(`usage: idea split-task <task> --title "<title>" --requirements <ref>[,<ref>...] ` +
			`[--statement <text>] (repeat --title/--requirements[/--statement] at least twice, in order) [--yes] [<initiative>]`)
	}
	titles := a.list("--title")
	reqLists := a.list("--requirements")
	statements := a.list("--statement")
	if len(titles) < 2 {
		return fmt.Errorf("split-task needs at least 2 replacement tasks; pass --title and --requirements twice or more")
	}
	if len(reqLists) != len(titles) {
		return fmt.Errorf("split-task got %d --title but %d --requirements; pass one --requirements "+
			"(comma-separated) per --title, in the same order", len(titles), len(reqLists))
	}
	if len(statements) > 0 && len(statements) != len(titles) {
		return fmt.Errorf("split-task got %d --title but %d --statement; pass one --statement per --title "+
			"(or omit --statement for every replacement), in the same order", len(titles), len(statements))
	}
	parts := make([]splitPart, len(titles))
	for i, title := range titles {
		title = strings.TrimSpace(title)
		if title == "" {
			return fmt.Errorf("replacement task %d needs a non-empty --title", i+1)
		}
		reqs := splitCSV(reqLists[i])
		if len(reqs) == 0 {
			return fmt.Errorf("replacement task %d (%q) needs at least one requirement in --requirements", i+1, title)
		}
		p := splitPart{Title: title, RequirementIDs: reqs}
		if len(statements) > 0 {
			p.Statement = statements[i]
		}
		parts[i] = p
	}

	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, a.positional[1:])
	if err != nil {
		return err
	}
	tasks, err := fetchTasks(c, projectID, iniID)
	if err != nil {
		return err
	}
	target, err := findTask(tasks, a.positional[0])
	if err != nil {
		return err
	}

	fmt.Fprintln(stderr, warn(fmt.Sprintf("⚠ splitting %s (%q) into %d tasks deletes it; its dependents and "+
		"outgoing dependencies carry over to every replacement.", target.Code, target.Title, len(parts))))
	if !a.has("--yes") {
		return fmt.Errorf("pass --yes to confirm splitting %s", target.Code)
	}

	var created []cliTask
	if err := c.do("POST", initiativeResource(projectID, iniID, "tasks/"+url.PathEscape(target.ID)+"/split"),
		map[string]any{"tasks": parts}, &created); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok(fmt.Sprintf("✓ split %s into %d tasks", target.Code, len(created))))
	for _, t := range created {
		fmt.Fprintln(stdout, "  "+code(t.Code)+" "+t.Title)
	}
	return nil
}

// cmdDependsOn replaces a task's depends_on wholesale: either `--on <ref>` one
// or more times, or `--clear` to make the task depend on nothing. Unlike
// PATCH's requirement_ids (omitted means "leave alone"), depends-on's whole
// purpose is the dependency set, so there is no "do nothing" default --
// requiring one of the two flags up front is cheaper than a round trip that
// comes back `depends_on is required`.
func cmdDependsOn(a cmdArgs, stdout, stderr io.Writer) error {
	if len(a.positional) < 1 {
		return fmt.Errorf("usage: idea depends-on <task> --on <ref> [--on <ref>...] [<initiative>] " +
			"(or --clear to depend on nothing)")
	}
	on := a.list("--on")
	clear := a.has("--clear")
	if len(on) == 0 && !clear {
		return fmt.Errorf("depends-on needs at least one --on <ref>, or --clear to remove every dependency")
	}
	if len(on) > 0 && clear {
		return fmt.Errorf("--on and --clear are contradictory; pass one or the other")
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, a.positional[1:])
	if err != nil {
		return err
	}
	tasks, err := fetchTasks(c, projectID, iniID)
	if err != nil {
		return err
	}
	target, err := findTask(tasks, a.positional[0])
	if err != nil {
		return err
	}
	// Pre-empt the self-dependency case the server also refuses
	// (handleSetTaskDependsOn: "a task cannot depend on itself") when the ref
	// resolves locally; refs that don't resolve here still reach the server's
	// own resolveTaskRefs, which answers the same way.
	for _, ref := range on {
		if dep, derr := findTask(tasks, ref); derr == nil && dep.ID == target.ID {
			return fmt.Errorf("a task cannot depend on itself (%s)", target.Code)
		}
	}
	// depends_on is a pointer server-side specifically so {} / omitted can be
	// told apart from an explicit empty list (handleSetTaskDependsOn's own
	// comment): `on` is nil here when --clear was given with no --on, and
	// encoding that as JSON null would be read as "omitted" and refused with
	// "depends_on is required" -- the opposite of what --clear asked for.
	deps := on
	if deps == nil {
		deps = []string{}
	}
	var updated cliTask
	if err := c.do("POST", initiativeResource(projectID, iniID, "tasks/"+url.PathEscape(target.ID)+"/depends-on"),
		map[string]any{"depends_on": deps}, &updated); err != nil {
		return err
	}
	if clear {
		fmt.Fprintln(stdout, ok("✓ cleared")+" every dependency on "+code(updated.Code))
		return nil
	}
	fmt.Fprintln(stdout, ok("✓ ")+code(updated.Code)+" now depends on "+strings.Join(codesFor(tasks, updated.DependsOn), ", "))
	return nil
}

// cmdReorderTasks sets the plan's task order from an ordered list of
// references. The server requires an exact permutation of the initiative's
// current tasks (store.ReorderTasks), so the count and membership are checked
// here first: a mismatch is reported against `idea tasks`'s own count instead
// of relayed as the server's "needs exactly N, got M".
//
// Unlike every other plan-editing verb, reorder's positional list is the
// ordered task list itself and its length is not fixed, so the optional
// initiative reference -- a trailing positional everywhere else in this
// package -- would be ambiguous against an arbitrary-length task list. It
// moves to a flag, --initiative, instead.
func cmdReorderTasks(a cmdArgs, stdout, stderr io.Writer) error {
	if len(a.positional) == 0 {
		return fmt.Errorf("usage: idea reorder <task1> <task2> ... [--initiative <ref>] " +
			"(name every task, in the new order; `idea tasks` lists the current one)")
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var iniArg []string
	if v := a.value("--initiative"); v != "" {
		iniArg = []string{v}
	}
	iniID, err := resolveInitiative(c, projectID, iniArg)
	if err != nil {
		return err
	}
	tasks, err := fetchTasks(c, projectID, iniID)
	if err != nil {
		return err
	}
	if len(a.positional) != len(tasks) {
		return fmt.Errorf("reorder needs exactly %d task(s) (the initiative's whole task list, in the new order), "+
			"got %d; `idea tasks` lists them", len(tasks), len(a.positional))
	}
	ids := make([]string, len(a.positional))
	seen := map[string]bool{}
	for i, ref := range a.positional {
		t, err := findTask(tasks, ref)
		if err != nil {
			return err
		}
		if seen[t.ID] {
			return fmt.Errorf("reorder lists %s more than once; name every task exactly once", t.Code)
		}
		seen[t.ID] = true
		ids[i] = t.ID
	}
	var updated []cliTask
	if err := c.do("POST", initiativeResource(projectID, iniID, "tasks/reorder"),
		map[string]any{"task_ids": ids}, &updated); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok(fmt.Sprintf("✓ reordered %d tasks", len(updated))))
	for i, t := range updated {
		fmt.Fprintf(stdout, "  %d. %s %s\n", i+1, code(t.Code), t.Title)
	}
	return nil
}
