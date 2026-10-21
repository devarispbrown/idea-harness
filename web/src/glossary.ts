// The glossary of domain terms a first-time user meets (P1.2). One place to
// look them up, rendered by GlossaryDialog and used by Term for inline
// tooltips. Kept deliberately short: one sentence each, in plain language.

export const GLOSSARY: { term: string; def: string }[] = [
  { term: "Initiative", def: "One idea you started — from its brief through to a verified change set." },
  { term: "PRD", def: "Product requirements: the reviewed list of what the thing must do." },
  { term: "Execution plan", def: "The ordered list of tasks that will build the thing, one slice at a time." },
  { term: "Task / slice", def: "One unit of the plan — a small, shippable piece the agent builds and you review." },
  { term: "Change set", def: "The diff the coding agent produced for one task, before it touches your repo." },
  { term: "Review findings", def: "What the adversarial reviewer (or a deterministic check) raised, with a severity and a recommendation." },
  { term: "Disposition", def: "Your verdict on a finding — fix it, accept the risk, it does not apply, defer, and so on." },
  { term: "System deny", def: "A deterministic safety boundary that overrides any approve — secrets, path escape, too many files, an open blocking finding." },
  { term: "Blast radius", def: "How many files one change set touches; capped so a change stays reviewable and revertable." },
  { term: "Convergence", def: "A review loop that settles — the reviewer accepts, or a decision's positions are reconciled." },
  { term: "Needs your judgment", def: "The pipeline stopped where a human must decide — approve, answer, or disposition." },
  { term: "Verify", def: "Recording evidence per requirement, so “done” means tested and matched, not just “code exists”." },
];
