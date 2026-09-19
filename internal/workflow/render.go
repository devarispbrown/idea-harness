package workflow

import (
	"fmt"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

func renderIdeaBrief(obj map[string]any) string {
	var b strings.Builder
	b.WriteString("# Idea Brief\n\n")
	if s := str(obj["title"]); s != "" {
		fmt.Fprintf(&b, "## %s\n\n", s)
	}
	if s := str(obj["summary"]); s != "" {
		fmt.Fprintf(&b, "%s\n\n", s)
	}
	kv := [][2]string{
		{"Primary user", str(obj["primary_user"])},
		{"Problem", str(obj["problem"])},
		{"Desired outcome", str(obj["outcome"])},
	}
	for _, p := range kv {
		if p[1] != "" {
			fmt.Fprintf(&b, "**%s:** %s\n\n", p[0], p[1])
		}
	}
	writeList(&b, "Constraints", strSlice(obj["constraints"]))
	writeList(&b, "Assumptions", strSlice(obj["assumptions"]))
	writeList(&b, "Open questions", strSlice(obj["open_questions"]))
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderResearchBrief(obj map[string]any) string {
	var b strings.Builder
	b.WriteString("# Research Brief\n\n")
	if s := str(obj["summary"]); s != "" {
		fmt.Fprintf(&b, "%s\n\n", s)
	}
	b.WriteString("## Findings\n\n")
	for _, f := range objSlice(obj["findings"]) {
		fmt.Fprintf(&b, "- **[%s]** %s _(confidence: %s, source: %s)_\n",
			str(f["type"]), str(f["statement"]), str(f["confidence"]), str(f["source"]))
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderPRD(obj map[string]any) string {
	var b strings.Builder
	b.WriteString("# Product Requirements Document\n\n")
	if s := str(obj["summary"]); s != "" {
		fmt.Fprintf(&b, "%s\n\n", s)
	}
	b.WriteString("## Requirements\n\n")
	for _, r := range objSlice(obj["requirements"]) {
		fmt.Fprintf(&b, "### %s\n\n", str(r["statement"]))
		b.WriteString("Acceptance criteria:\n\n")
		for _, ac := range objSlice(r["acceptance_criteria"]) {
			fmt.Fprintf(&b, "- %s %s (%s)\n", str(ac["metric"]), str(ac["target"]), str(ac["condition"]))
		}
		b.WriteString("\n")
	}
	writeList(&b, "Risks", strSlice(obj["risks"]))
	writeList(&b, "Open questions", strSlice(obj["open_questions"]))
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderRequirements(reqs []domain.Requirement) string {
	if len(reqs) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, r := range reqs {
		fmt.Fprintf(&b, "- %s: %s\n", r.Code, r.Statement)
		for _, ac := range r.AcceptanceCriteria {
			fmt.Fprintf(&b, "  - %s %s (%s)\n", ac.Metric, ac.Target, ac.Condition)
		}
	}
	return b.String()
}

func renderDecisions(decs []domain.Decision) string {
	if len(decs) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, d := range decs {
		if d.Status == domain.DecisionDecided && d.Resolution != "" {
			fmt.Fprintf(&b, "- %s: %s (DECIDED: %s)\n", d.Code, d.Question, d.Resolution)
		} else {
			fmt.Fprintf(&b, "- %s: %s (recommendation: %s)\n", d.Code, d.Question, d.Recommendation)
		}
	}
	return b.String()
}

// renderFindings renders prior review findings (with human disposition + note)
// so the product manager can incorporate the human's judgment on the next
// revise cycle.
func renderFindings(findings []domain.Finding) string {
	if len(findings) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, f := range findings {
		fmt.Fprintf(&b, "- [%s] %s", f.Severity, f.Finding)
		if f.Disposition != "" {
			fmt.Fprintf(&b, " (disposition: %s", f.Disposition)
			if f.Note != "" {
				fmt.Fprintf(&b, "; human note: %s", f.Note)
			}
			b.WriteString(")")
		} else if f.Note != "" {
			fmt.Fprintf(&b, " (human note: %s)", f.Note)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderDecisionForDiscussion renders a single decision for a discussion turn.
func renderDecisionForDiscussion(d *domain.Decision) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Question: %s\n", d.Question)
	if len(d.Options) > 0 {
		b.WriteString("Options:\n")
		for _, o := range d.Options {
			fmt.Fprintf(&b, "  - %s\n", o)
		}
	}
	if d.Recommendation != "" {
		fmt.Fprintf(&b, "Recommendation: %s\n", d.Recommendation)
	}
	if d.Status == domain.DecisionDecided && d.Resolution != "" {
		fmt.Fprintf(&b, "Resolved: %s\n", d.Resolution)
	}
	return b.String()
}

// renderDecisionDiscussion renders the prior discussion thread for context.
func renderDecisionDiscussion(thread []domain.DecisionDiscussion) string {
	if len(thread) == 0 {
		return "(no prior discussion)"
	}
	var b strings.Builder
	for _, m := range thread {
		fmt.Fprintf(&b, "%s: %s\n", m.Author, m.Message)
	}
	return b.String()
}

func renderArchitecture(obj map[string]any) string {
	var b strings.Builder
	b.WriteString("# Technical Design\n\n")
	if s := str(obj["summary"]); s != "" {
		fmt.Fprintf(&b, "%s\n\n", s)
	}
	b.WriteString("## Components\n\n")
	for _, c := range objSlice(obj["components"]) {
		fmt.Fprintf(&b, "- **%s** — %s\n", str(c["name"]), str(c["responsibility"]))
		for _, dep := range strSlice(c["depends_on"]) {
			fmt.Fprintf(&b, "  - depends on: %s\n", dep)
		}
	}
	writeList(&b, "Data flow", strSlice(obj["data_flow"]))
	writeList(&b, "Risks", strSlice(obj["risks"]))
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderProductDesign(obj map[string]any) string {
	var b strings.Builder
	b.WriteString("# Product Design\n\n")
	if s := str(obj["summary"]); s != "" {
		fmt.Fprintf(&b, "%s\n\n", s)
	}
	b.WriteString("## Flows\n\n")
	for _, f := range objSlice(obj["flows"]) {
		fmt.Fprintf(&b, "- **%s**", str(f["name"]))
		if g := str(f["goal"]); g != "" {
			fmt.Fprintf(&b, " — %s", g)
		}
		b.WriteString("\n")
		for _, s := range strSlice(f["steps"]) {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
	}
	writeList(&b, "Screens", strSlice(obj["screens"]))
	writeList(&b, "Risks", strSlice(obj["risks"]))
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderVerificationPlan(obj map[string]any) string {
	var b strings.Builder
	b.WriteString("# Verification Plan\n\n")
	if s := str(obj["summary"]); s != "" {
		fmt.Fprintf(&b, "%s\n\n", s)
	}
	b.WriteString("## Verification hooks\n\n")
	for _, h := range objSlice(obj["hooks"]) {
		fmt.Fprintf(&b, "- **%s** → %s (%s)", str(h["requirement"]), str(h["check"]), str(h["evidence"]))
		// [tests: ...] is a machine-recoverable suffix (C2, wave 3 contract):
		// this rendered markdown is the artifact's only persisted form (the raw
		// model JSON is not kept), so mapping a test name to a requirement later
		// (evidence_tests on GET .../requirements) parses it back out of here --
		// see internal/projection.mappedTestsByRequirement.
		if tests := strSlice(h["tests"]); len(tests) > 0 {
			fmt.Fprintf(&b, " [tests: %s]", strings.Join(tests, ", "))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderExecutionPlan(obj map[string]any) string {
	var b strings.Builder
	b.WriteString("# Execution Plan\n\n")
	if s := str(obj["summary"]); s != "" {
		fmt.Fprintf(&b, "%s\n\n", s)
	}
	b.WriteString("## Tasks\n\n")
	for _, t := range objSlice(obj["tasks"]) {
		fmt.Fprintf(&b, "- [%s] **%s** (%s)\n", str(t["priority"]), str(t["title"]), str(t["requirement"]))
		if s := str(t["statement"]); s != "" {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
		if deps := strSlice(t["depends_on"]); len(deps) > 0 {
			fmt.Fprintf(&b, "  - depends on: %s\n", strings.Join(deps, ", "))
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderTasks(tasks []domain.Task) string {
	if len(tasks) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, t := range tasks {
		fmt.Fprintf(&b, "- %s: %s [%s]\n", t.Code, t.Title, t.Priority)
		if t.Statement != "" {
			fmt.Fprintf(&b, "  - %s\n", t.Statement)
		}
	}
	return b.String()
}

// renderQuestions renders existing questions (open + answered) so the product
// manager can avoid re-raising them on the next cycle.
func renderQuestions(questions []domain.Question) string {
	if len(questions) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, q := range questions {
		state := "open"
		if q.Status == domain.QuestionResolved {
			state = "answered"
		}
		fmt.Fprintf(&b, "- [%s] %s\n", state, q.Question)
	}
	return b.String()
}

func writeList(b *strings.Builder, heading string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "## %s\n\n", heading)
	for _, it := range items {
		fmt.Fprintf(b, "- %s\n", it)
	}
	b.WriteString("\n")
}
