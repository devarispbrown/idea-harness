package workflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
)

// This file pins the fix for the change-set adversarial review truncation
// defect (round 6): a review against a moderately sized diff hit the model's
// output-token ceiling, runModel raised model.ErrTruncated, and the change
// set was left wedged -- review_required with ReviewCompletedAt nil, the task
// never marked done (so `idea execute` kept re-offering it and hit the same
// "never finished its adversarial review" guard every time), and no supported
// way forward that did not mean hand-editing harness.yaml. The fix has three
// layers, tested here at the workflow layer (the model-layer capability
// accuracy is pinned in internal/model/maxoutput_pin_test.go):
//
//  1. capability accuracy (internal/model): a real model's output ceiling is
//     far higher than the old flat constant -- tested there, not here.
//  2. a reachable budget: review.max_tokens now actually raises the request
//     sent, and the error names it (effectiveMaxOutput, wrapTruncationGuidance
//     in engine.go; see maxoutput_test.go for the pure-function coverage and
//     TestTruncationErrorNamesOnlyReachableActions below for the message).
//  3. no wedge, no silent truncation: a truncation is retried once with a
//     smaller ask (runModel), and a change-set review that still cannot
//     complete is recorded explicitly as "unavailable" rather than failing
//     the whole run, which is what gives --accept-findings somewhere to
//     attach (domain.ChangeSet.ReviewUnavailable).

// fakeProviderOf returns the FakeProvider newExecuteFixture/newWorkspaceFixture
// wire into eng's registry under the demo key, so a test built from one of
// those fixtures can script a provider failure (EnqueueError/EnqueueResponse)
// after the engine already exists.
func fakeProviderOf(t *testing.T, eng *Engine) *model.FakeProvider {
	t.Helper()
	p, ok := eng.registry.Get(demoModelSpec)
	if !ok {
		t.Fatal("test fixture: no provider registered under the demo key")
	}
	fp, ok := p.(*model.FakeProvider)
	if !ok {
		t.Fatalf("test fixture: registered provider is %T, not *model.FakeProvider", p)
	}
	return fp
}

// truncatedForTest builds the error a real provider returns when a reply is
// cut off at its output-token budget -- the same shape (wrapping
// model.ErrTruncated) FakeProvider.EnqueueError needs to make a scripted
// attempt behave like a real truncated one, without a real API key.
func truncatedForTest(sent int) error {
	return fmt.Errorf("anthropic: %w: model %q hit its %d-token output budget and was cut off mid-structure, "+
		"so the reply is not safe to use", model.ErrTruncated, "claude-sonnet-5", sent)
}

// TestChangeSetReviewRetriesOnceOnTruncationThenSucceeds is layer 3's first
// line of defense: truncation is not retried unchanged (the same request
// truncates the same way every time), so runModel asks for less on the
// SECOND attempt. A review that truncates once and then fits must complete
// normally, with real findings -- it must not degrade to "unavailable" just
// because the first attempt truncated.
func TestChangeSetReviewRetriesOnceOnTruncationThenSucceeds(t *testing.T) {
	eng, db, iniID, _ := newWorkspaceFixture(t)
	addTask(t, db, iniID, "add /health")
	fp := fakeProviderOf(t, eng)
	fp.EnqueueError(ChangeSetReviewSchemaName, truncatedForTest(16384))
	fp.EnqueueResponse(ChangeSetReviewSchemaName, `{"findings":[]}`)

	ctx := context.Background()
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !cs.ReviewCompleted() {
		t.Fatal("expected a completed review once the retry succeeded")
	}
	if cs.ReviewUnavailable() {
		t.Fatal("the review completed on retry; it must not also be marked unavailable")
	}

	var reviewCalls int
	var sawRetryInstruction bool
	for _, r := range fp.Calls() {
		if r.JSONSchemaName != ChangeSetReviewSchemaName {
			continue
		}
		reviewCalls++
		if strings.Contains(r.System, "cut off because it exceeded the output budget") {
			sawRetryInstruction = true
		}
	}
	if reviewCalls != 2 {
		t.Fatalf("expected exactly 2 review calls (1 truncated + 1 retry with a smaller ask), got %d", reviewCalls)
	}
	if !sawRetryInstruction {
		t.Error("the retried call's system prompt does not ask for a smaller reply")
	}
}

// TestChangeSetReviewExhaustedTruncationDegradesInsteadOfFailing is layer 3's
// second line of defense, and the core of the wedge fix: when EVERY attempt
// (including the one retry above) truncates, Execute must not return a hard
// error that leaves the change set in review_required with no record of why
// and the task never marked done -- that is exactly what wedged `idea
// execute` in the original defect. It must instead record the cause
// explicitly and let the run finish.
func TestChangeSetReviewExhaustedTruncationDegradesInsteadOfFailing(t *testing.T) {
	eng, db, iniID, _ := newWorkspaceFixture(t)
	task := addTask(t, db, iniID, "add /health")
	fp := fakeProviderOf(t, eng)
	for i := 0; i < 3; i++ {
		fp.EnqueueError(ChangeSetReviewSchemaName, truncatedForTest(16384))
	}

	ctx := context.Background()
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatalf("Execute must not fail the whole run on an exhausted truncation: %v", err)
	}
	if cs.ReviewCompleted() {
		t.Fatal("expected ReviewCompleted() false -- the review never actually finished")
	}
	if !cs.ReviewUnavailable() {
		t.Fatal("expected ReviewUnavailable() true, recording that the review was attempted and exhausted")
	}
	cause := cs.ReviewNeverCompletedCause()
	if cause == "likely cancelled mid-review" {
		t.Errorf("ReviewNeverCompletedCause still reports the generic cancellation guess, not the real reason: %q", cause)
	}
	if !strings.Contains(cause, "truncat") && !strings.Contains(cause, "output budget") {
		t.Errorf("ReviewNeverCompletedCause does not name the real reason: %q", cause)
	}

	// No wedge: `idea execute` must not keep re-offering this task.
	gotTask, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotTask.Status != domain.TaskDone {
		t.Fatalf("expected the task marked done, got %q -- a task left otherwise stays re-pickable forever", gotTask.Status)
	}
}

// TestReviewUnavailableCanBeApprovedWithAcceptFindingsOnly is layer 3's third
// and final piece, and the mutation check on the new guard: once a review is
// recorded unavailable, the ONE supported way forward is an explicit,
// recorded human override (--accept-findings), never a plain approve. A
// plain approve refusing here, and only --accept-findings succeeding, is
// what proves ReviewUnavailable actually gates the new escape rather than
// silently widening ReviewCompleted's own meaning.
func TestReviewUnavailableCanBeApprovedWithAcceptFindingsOnly(t *testing.T) {
	eng, db, iniID, _ := newWorkspaceFixture(t)
	addTask(t, db, iniID, "add /health")
	fp := fakeProviderOf(t, eng)
	for i := 0; i < 3; i++ {
		fp.EnqueueError(ChangeSetReviewSchemaName, truncatedForTest(16384))
	}
	ctx := context.Background()
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.ReviewUnavailable() {
		t.Fatal("setup: expected the review to be recorded unavailable")
	}

	// Mutation check: a plain approve must still refuse.
	if err := eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved"); err == nil {
		t.Fatal("plain approve succeeded on a change set whose review is only unavailable, not completed")
	} else if !strings.Contains(err.Error(), "rerun") {
		t.Errorf("plain-approve refusal does not name a way forward: %v", err)
	}
	if got, gerr := db.GetChangeSet(ctx, cs.ID); gerr != nil {
		t.Fatal(gerr)
	} else if got.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("the refused plain approve moved the change set to %q", got.Status)
	}

	// The supported escape.
	if err := eng.ReviewChangeSetAccepting(ctx, iniID, cs.ID, "approved"); err != nil {
		t.Fatalf("--accept-findings refused a change set with an explicitly recorded, exhausted review: %v", err)
	}
	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetApproved {
		t.Fatalf("expected approved, got %q", got.Status)
	}
}

// TestTruncationErrorNamesOnlyReachableActions pins layer 2's message half:
// the remediation a truncation error suggests must name something a human
// can actually do. Before this fix, model.truncatedError said "raise the
// stage budget", and harness.yaml's review.max_tokens -- the one field that
// sounds like it does that -- did nothing (effectiveMaxOutput never read
// it). It does now (see effectiveMaxOutput's doc comment), so naming it here
// is no longer a dead end.
func TestTruncationErrorNamesOnlyReachableActions(t *testing.T) {
	err := wrapTruncationGuidance(truncatedForTest(16384))
	msg := err.Error()
	if !strings.Contains(msg, "review.max_tokens") {
		t.Errorf("truncation error does not name the one config field that actually raises the budget: %v", err)
	}
	if !errors.Is(err, model.ErrTruncated) {
		t.Error("wrapTruncationGuidance must not break errors.Is(err, model.ErrTruncated)")
	}

	// A non-truncation error must pass through completely unchanged: this
	// function must never invent guidance for a failure it does not
	// understand.
	other := errors.New("some other model failure")
	if got := wrapTruncationGuidance(other); got != other {
		t.Errorf("wrapTruncationGuidance altered a non-truncation error: got %v, want it unchanged", got)
	}
}

// TestChangeSetReviewRecoversFromARealAnthropicTruncatedThinkingReply pins
// D1 of the 2026-09-21 live-probe follow-up, end-to-end through the REAL
// Anthropic client (not FakeProvider's synthetic errors used above): a lone
// "thinking" content block with no text block at all, stop_reason
// "max_tokens" -- the exact shape observed on the wire for claude-sonnet-5
// when it is cut off before it finishes reasoning -- must still become
// model.ErrTruncated, so every layer above (runModel's retry-with-a-smaller-
// ask, ErrReviewUnavailable, domain.ChangeSet.ReviewUnavailable, the
// --accept-findings escape) actually fires.
//
// Before the fix, AnthropicProvider.Complete checked for empty content
// BEFORE checking stop_reason, so this exact reply produced a generic
// "anthropic: empty content (stop_reason=max_tokens, ...)" error that never
// satisfied errors.Is(err, model.ErrTruncated) -- runModel's retry never
// triggered (isTransientModelError is also false for this message), the
// change set was left wedged in review_required with ReviewCompletedAt nil,
// and ReviewNeverCompletedCause fell back to "likely cancelled mid-review":
// a truncation reported as a cancellation, with --accept-findings refused
// because ReviewCompleted() and ReviewUnavailable() were both false.
func TestChangeSetReviewRecoversFromARealAnthropicTruncatedThinkingReply(t *testing.T) {
	var requests []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, string(body))
		// The probe's own observed wire values: {"max_tokens": 4096,
		// "stop_reason": "max_tokens", "out_tokens": 4096,
		// "blocks": [["thinking", 0]]} -- a lone thinking block, no text
		// block, and output_tokens equal to the budget (a length-stop, not
		// the model choosing to stop on its own).
		_, _ = w.Write([]byte(`{"content":[{"type":"thinking","thinking":"partial reasoning, cut off mid-thought"}],` +
			`"stop_reason":"max_tokens","usage":{"input_tokens":100,"output_tokens":4096},"model":"claude-sonnet-5"}`))
	}))
	defer ts.Close()

	eng, db, iniID, _ := newWorkspaceFixture(t)
	addTask(t, db, iniID, "add /health")
	// Route ONLY the adversarial-review role to a real AnthropicProvider
	// pointed at the stub above, so this test exercises anthropic.go's
	// actual response parsing for that call while every other stage
	// (implementation_brief/planner, ...) keeps using the demo FakeProvider's
	// synthetic-but-valid responses -- Execute calls more than one role, and
	// only the review role's truncation is what this test is about.
	eng.registry.Add("review-stub", model.NewAnthropic("sk-test-not-real", ts.URL))
	eng.cfg.Models = map[string]string{RoleAdversarialReviewer: "review-stub/claude-sonnet-5"}

	ctx := context.Background()
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatalf("Execute must not hard-fail on an exhausted truncation: %v", err)
	}

	if len(requests) != 3 {
		t.Fatalf("expected exactly 3 review requests (1 + 1 retry with a smaller ask + 1 final exhausted attempt), got %d", len(requests))
	}
	if !strings.Contains(requests[1], "cut off because it exceeded the output budget") {
		t.Error("the retried request's system prompt does not ask for a smaller reply -- the retry-on-truncation path did not fire")
	}

	if cs.ReviewCompleted() {
		t.Fatal("expected ReviewCompleted() false -- every attempt truncated")
	}
	if !cs.ReviewUnavailable() {
		t.Fatal("expected ReviewUnavailable() true, recording that the review was attempted and exhausted")
	}
	cause := cs.ReviewNeverCompletedCause()
	if cause == "likely cancelled mid-review" {
		t.Errorf("ReviewNeverCompletedCause still reports the generic cancellation guess, not the real reason: %q", cause)
	}
	if !strings.Contains(cause, "truncat") && !strings.Contains(cause, "output budget") {
		t.Errorf("ReviewNeverCompletedCause does not name truncation: %q", cause)
	}

	// The documented escape must be reachable, and ONLY it -- a plain approve
	// must still refuse.
	if err := eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved"); err == nil {
		t.Fatal("plain approve succeeded on a change set whose review is only unavailable, not completed")
	}
	if err := eng.ReviewChangeSetAccepting(ctx, iniID, cs.ID, "approved"); err != nil {
		t.Fatalf("--accept-findings refused a change set with a genuinely exhausted, recorded review: %v", err)
	}
	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetApproved {
		t.Fatalf("expected approved, got %q", got.Status)
	}
}

// TestRunFailsCleanlyWhenFramingTruncatesRepeatedly is the "other stages"
// half of the investigation: a stage outside the change-set review has no
// ChangeSet to record "unavailable" against, so an exhausted truncation there
// must still fail the run -- but with the same reachable message, not the
// old unreachable one, and without hanging or looping past the 3-attempt
// budget.
func TestRunFailsCleanlyWhenFramingTruncatesRepeatedly(t *testing.T) {
	fp := model.NewFakeProvider()
	for i := 0; i < 3; i++ {
		fp.EnqueueError("idea_brief", truncatedForTest(4096))
	}
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	_, err := eng.Run(ctx, ini.ID, ini.Description)
	if err == nil {
		t.Fatal("expected Run to fail once every framing attempt truncated")
	}
	if !strings.Contains(err.Error(), "review.max_tokens") {
		t.Errorf("framing truncation error does not name a reachable fix: %v", err)
	}
}
