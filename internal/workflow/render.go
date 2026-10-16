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
	if objs := objSlice(obj["objectives"]); len(objs) > 0 {
		b.WriteString("## Objectives\n\n")
		for _, o := range objs {
			title := str(o["statement"])
			if k := str(o["key"]); k != "" {
				title = k + ": " + title
			}
			fmt.Fprintf(&b, "### %s\n\n", title)
			if r := str(o["rationale"]); r != "" {
				fmt.Fprintf(&b, "%s\n\n", r)
			}
			b.WriteString("Metrics:\n\n")
			for _, m := range objSlice(o["metrics"]) {
				fmt.Fprintf(&b, "- %s: %s", str(m["name"]), str(m["direction"]))
				if v, ok := num(m["baseline"]); ok {
					fmt.Fprintf(&b, " from %s", domain.FormatMetricValue(v))
				}
				if v, ok := num(m["target"]); ok {
					fmt.Fprintf(&b, " to %s", domain.FormatMetricValue(v))
				}
				if u := str(m["unit"]); u != "" {
					fmt.Fprintf(&b, " %s", u)
				}
				if v, ok := num(m["guardrail"]); ok {
					fmt.Fprintf(&b, " (guardrail %s)", domain.FormatMetricValue(v))
				}
				if h := str(m["hypothesis"]); h != "" {
					fmt.Fprintf(&b, " -- hypothesis: %s", h)
				}
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("## Requirements\n\n")
	for _, r := range objSlice(obj["requirements"]) {
		fmt.Fprintf(&b, "### %s\n\n", str(r["statement"]))
		if serves := strSlice(r["objectives"]); len(serves) > 0 {
			fmt.Fprintf(&b, "Serves: %s\n\n", strings.Join(serves, ", "))
		}
		if basis := strSlice(r["based_on"]); len(basis) > 0 {
			fmt.Fprintf(&b, "Rests on: %s\n\n", strings.Join(basis, ", "))
		}
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

// renderQuestionsWithAnswers renders existing questions (open + answered)
// with the human's actual answer text for every resolved one, so a model
// reading this context can avoid re-raising an answered question AND act on
// what was actually decided (ICP re-run defect H: "the human's answers ...
// never reached the implementer, which built a JSON config file" -- because
// nothing downstream ever saw more than "[answered] <question>". The answer
// is resolved from the redacted blob store by AnswerQuestion's own digest --
// the same store runPlanner/runProductDefinition already read other content
// from -- so a missing/unreadable blob degrades to "(answer not found)"
// rather than failing the whole review or plan.
func (e *Engine) renderQuestionsWithAnswers(questions []domain.Question) string {
	if len(questions) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, q := range questions {
		if q.Status != domain.QuestionResolved {
			fmt.Fprintf(&b, "- [open] %s\n", q.Question)
			continue
		}
		fmt.Fprintf(&b, "- [answered] %s -- ANSWER: %s\n", q.Question, e.answerText(q))
	}
	return b.String()
}

// answerText resolves a resolved question's actual answer text from the
// redacted blob store by its AnswerDigest (ICP re-run defect H) -- "(answer
// not found)" when there is no digest, no blob store, or the blob cannot be
// read, so a lookup failure degrades the rendered context rather than
// failing the review/plan/brief it feeds.
//
// Read from the REDACTED store, which is where both writers put an answer
// (handleAnswerQuestion's pr.blobs.Put, storeBlob). This used to read
// contentBlobs() -- the redaction-free raw store -- which in the daemon is a
// different directory, so every answer resolved to "(answer not found)"
// outside tests (whose engine wires one store as both). Found auditing every
// raw-blobs read path: the raw store holds file content for apply/revert and
// nothing a prompt should be built from.
func (e *Engine) answerText(q domain.Question) string {
	blobs := e.snapshotConfig().blobs
	if q.AnswerDigest != "" && blobs != nil {
		if data, err := blobs.Get(q.AnswerDigest); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return "(answer not found)"
}

// resolvedDecisionsAndAnswers renders ONLY the decided decisions and answered
// questions (never the open ones -- this is meant for an "authoritative,
// follow these exactly" section, and an open item has nothing to follow yet)
// as a flat list, for deterministic (orchestrator-authored, not
// model-dependent) inclusion in the implementer brief (ICP re-run defect H).
// "" when there is nothing resolved yet.
func (e *Engine) resolvedDecisionsAndAnswers(decs []domain.Decision, questions []domain.Question) string {
	var b strings.Builder
	for _, d := range decs {
		if d.Status == domain.DecisionDecided && d.Resolution != "" {
			fmt.Fprintf(&b, "- %s: %s -- DECIDED: %s\n", d.Code, d.Question, d.Resolution)
		}
	}
	for _, q := range questions {
		if q.Status != domain.QuestionResolved {
			continue
		}
		fmt.Fprintf(&b, "- %s -- ANSWERED: %s\n", q.Question, e.answerText(q))
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
