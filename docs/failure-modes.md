# Failure modes

What to do when `idea` refuses to run something, or a change set just sits
there. Five classes, each seen in a real run. Format: **what you saw → what to
run → what it tells you → how to get unstuck.**

Every message below is quoted from the code that emits it — some are
templated (`%s`), so the shape is shown first, then a worked example with
real-looking ids. `internal/docsgen`'s `TestFailureModesDocMessagesMatchCode`
checks this page against the source on every build; if a message is reworded
in code and not here, that test fails.

## 1. A change set stuck without a finished review

**Symptom A — `idea status` keeps pointing at the same change set:**

```
→ Rerun the review for CHG-7
  CHG-7 never finished its adversarial review (likely cancelled mid-review),
  so it has no findings to approve yet. `idea review CHG-7 rerun` runs it.
```

**Symptom B — `idea execute` refuses instead of starting:**

```
$ idea execute
idea: workflow: execute: CHG-7 never finished its adversarial review
(likely cancelled mid-review) and is still awaiting one; run `idea review
CHG-7 rerun` to finish reviewing it before running this task again
```

These are **two symptoms of one cause**, not two defects: `internal/workflow/execute.go`'s
own guard is `Status == ChangeSetReviewRequired && !ReviewCompleted()`. It fires when
`Execute`'s call to the adversarial reviewer itself errored — almost always the daemon
(or its operation) was stopped mid-review — after the change set had already moved to
`review_required`. The change set is left holding only mechanical-check findings, no
adversarial pass, and `idea execute` will not spend a second paid run on the same task
while that's true.

**Diagnostic:**

```
idea changes
```

**Expected output:** the change set's line reads `never finished its adversarial
review`, not `needs your approval` — `idea changes` and `idea status` share the same
rule for telling the two apart (`cs.ReviewCompletedAt == nil`), so they never disagree.

**Recovery:**

```
idea review CHG-7 rerun
```

This is a real, paid model call — it prints `✓ review rerun started`, then waits (or
hand back immediately with `--no-wait`). Once it finishes, the change set either has
real findings to act on, or hits the output-ceiling case in §5 below.

The only other way a `ReviewNeverCompletedCause()` message differs: a change set
created before this project's daemon added review-completion tracking reads
"it predates review-completion tracking from before this project's daemon was
upgraded, not because anything was cancelled" instead of "likely cancelled
mid-review" — same guard, an honest label for a different (older, harmless) cause.

## 2. A change set the secret sweep rejected

**Symptom:** `idea execute` said it started, and the task comes back `rejected`
with no explanation in sight.

**Diagnostic:**

```
idea reviews
```

**Expected output** — one `critical` finding naming where, never the value:

```
critical  config/settings.py:42: looks like AWS access key ID
      → remove the sensitive content and re-execute, or, if this is not
        really a secret, disposition this finding "not a secret" to allow
        it from now on
```

(A match inside a check's own output, rather than a file, reads `in the output of
<check>: looks like <kind>` instead of `path:line: looks like <kind>`.)

**Recovery, case 1 — it is a real secret:** remove it from the file (or move it to
`.env`, which is never copied into the agent's workspace) and run `idea execute`
again. The rejected change set is terminal; nothing needs to be cleared first.

**Recovery, case 2 — it is a false positive** (a fixture, a test key, a placeholder
that merely matches a known key *shape*): disposition the finding `not_a_secret`.
There is no CLI verb for this yet — `idea reviews` only lists findings — so today
it is one API call:

```
POST /api/v1/projects/{projectId}/findings/{id}/disposition
{"disposition": "not_a_secret"}
```

(`idea reviews --json` gives you the finding's `id`; see `docs/api.md` for how to
authenticate the call.) **What this does and does not unblock:** the allowlist entry
is scoped to the exact fingerprint *and the exact file path* it was raised at — not
the whole project, and not even the same secret value pasted into a different file.
A value drawn from the project's own configured secrets (not a pattern-shaped guess)
is never eligible for this override at all; the store refuses it outright rather than
re-denying it silently on the next run.

## 3. A no-op change set — zero files changed

**Symptom:** the task looks stuck at "proposed" and never reaches done, with no
rejection and no pending review to act on.

**Diagnostic:**

```
idea reviews
```

**Expected output:**

```
medium    The coding agent finished without changing any file.
      → Read the agent output. A 401 means the provider key is wrong; a 404
        means the model id is retired. Fix it and retry, or approve the
        empty change set if the task was already done.
```

A change set that touches zero files is never treated as "this task is done" —
applying it leaves the task re-pickable on purpose, so a task genuinely already
done (the agent correctly found nothing to change) doesn't get stuck, but a
silently failed agent run doesn't get *counted* as progress either.

**Recovery:** read the Evidence on that finding — it's the end of the agent's own
output, which is usually what actually happened (a bad key, a retired model id,
or a task that was already satisfied). Then:

```
idea review CHG-8 reject      # it was a real failure — try again after fixing the cause
idea review CHG-8 approve     # the task really was already done
idea apply CHG-8
```

If instead you re-run `idea execute` while this change set is still open
(unreviewed, or reviewed but not yet applied), it refuses:

```
idea: workflow: execute: CHG-8 is a no-op change set for TASK-3 and is still
review_required; reject it, or approve and apply it, before running this
task again
```

## 4. A revert refused because the workspace moved on

**Symptom:** `idea revert CHG-4` fails instead of rolling back.

```
$ idea revert CHG-4
idea: revert refused: the workspace changed since CHG-4 was applied:
src/app.js: content differs from the applied change set's post-image; ...
revert CHG-9 first; discard (or stash) your edits to src/app.js, then
revert again
```

That is the real shape (`revertRefusedMessage(humanID, stale, laterByFile)`,
three slots: the change set, what `fsdiff.StalePost` found per file, and the
advice). The advice differs per file: if a **later, still-applied change
set** touched the same file, the fix is to revert that one first; otherwise
it's your own edit in the way, and the fix is to discard or stash it.

The same refusal also records a `stale_post` finding (`idea reviews` shows it)
so the reason outlives the failed command, carrying the same advice:

```
observation  The workspace changed after this change set was applied, so
             reverting it now would erase that work.
      → revert CHG-9 first; discard (or stash) your edits to src/app.js,
        then revert again
```

Nothing is lost by the refusal itself — the check runs, and the refusal is
printed, before anything is written.

**Diagnostic:** none needed beyond the refusal itself — it is synchronous
(checked before the operation starts, not after), so nothing is left half-done.

**Recovery:**

```
idea revert CHG-9     # if a later change set touched the same file(s)
# or
git stash             # if it's your own edit
idea revert CHG-4     # then retry
```

There is no force flag. The recovery is always to resolve the conflicting
edit yourself, then revert again.

## 5. A review that hit the model's output ceiling

**Symptom:** same as §1's "never finished its adversarial review," but the
cause names a token budget instead of a cancellation:

```
→ Rerun the review for CHG-12
  CHG-12 never finished its adversarial review (the adversarial review could
  not complete: workflow: review: ...: model "..." hit its 32000-token
  output budget and was cut off mid-structure, so the reply is not safe to
  use; raise `review.max_tokens` in harness.yaml to ask for a larger reply
  and retry this stage, or reduce the size of what is being sent to it),
  so it has no findings to approve yet. `idea review CHG-12 rerun` runs it.
```

This happens when the change set's diff is large enough that the review's
structured reply gets cut off at the model's real output ceiling — even
after the engine's own one-time retry asking for a shorter reply. Unlike §1,
the task is already marked `done` here (the review was genuinely attempted,
not merely interrupted), so `idea execute` will not re-pick it; there is
nothing to re-run except the review itself.

`review.max_tokens` in `harness.yaml` is the reachable fix the message
points at: it overrides the review stage's own request size outright (not
just a ceiling), still bounded by the model's real limit. See
`docs/configuration.md` for the full field reference.

**Diagnostic:**

```
idea status
```

**Expected output:** the guidance block above, naming the cause and `idea
review <id> rerun` as the way forward.

**Recovery, in order:**

1. Raise `review.max_tokens` in `.idea-harness/harness.yaml` and run
   `idea review CHG-12 rerun` — a fresh, paid review at the larger budget.
2. If it truncates again, proceed without a completed review instead.
   A plain approve still refuses:

   ```
   $ idea review CHG-12 approve
   idea: workflow: cannot approve CHG-12: its adversarial review never
   completed (the adversarial review could not complete: ...), so it
   carries only mechanical check observations, no adversarial findings;
   run `idea review CHG-12 rerun` to try again, or `idea review CHG-12
   approve --accept-findings` to proceed without a completed review
   ```

   `--accept-findings` is allowed here specifically *because* a review was
   genuinely attempted and explicitly recorded as unable to finish — a
   change set that was merely cancelled (§1) cannot take this path; only an
   explicitly-recorded truncation can:

   ```
   idea review CHG-12 approve --accept-findings
   ```
