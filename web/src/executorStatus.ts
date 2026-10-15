// Pure formatting for the Settings → Executor status panel (C3), extracted
// for the same reason providerStatus.ts and statusChips.ts are: the copy
// decisions are worth unit testing without a DOM.
//
// The shape mirrors the daemon's `status` object on GET .../executors
// (contract added alongside this feature by another worker): `problem` is
// empty exactly when the executor is ready to run, and image/key detail is
// advisory underneath that headline fact.

export interface ExecutorStatusInput {
  image: string;
  image_present: boolean;
  key_env: string;
  key_set: boolean;
  problem: string;
  image_state?: "present" | "absent" | "unknown" | "docker_unavailable";
  // Information that does not block a run (e.g. an image the first run will
  // download).
  note?: string;
  // S2: the project's newest smoke test, for the SELECTED executor only
  // (omitted for every other row in the list, and for one that has never
  // run). Settings reads this to show "Last smoke test passed 5 min ago"
  // on load, surviving a reload that lost track of any in-flight operation.
  last_smoke?: {
    status: string;
    finished_at?: string;
    error?: string;
  };
}

export interface ExecutorStatusLine {
  text: string;
  ok: boolean;
}

export interface ExecutorStatusLines {
  // Omitted for a kind that runs no image at all (internal): there is nothing
  // to name, and "the pinned default image is pulled." under the placeholder
  // executor claimed an image that does not exist.
  imageLine?: ExecutorStatusLine;
  // Omitted for a keyless executor kind (e.g. one that needs no provider
  // credential at all) rather than shown as a false "missing key".
  keyLine?: ExecutorStatusLine;
  // Omitted when the executor is ready.
  problemLine?: string;
  noteLine?: string;
}

export function executorStatusLines(status: ExecutorStatusInput, defaultImage = ""): ExecutorStatusLines {
  const imageName = status.image || defaultImage || "the pinned default image";
  // When Docker itself is unreachable the image cannot be checked, and saying
  // it "is not pulled" under a "Docker is not running" problem contradicted it.
  const dockerDown =
    status.image_state === "docker_unavailable" || /docker is not running/i.test(status.problem || "");
  const usesImage = !!(status.image || defaultImage || status.image_state || !status.image_present);
  const imageLine: ExecutorStatusLine | undefined = !usesImage
    ? undefined
    : status.image_present
    ? { text: `${imageName} is pulled.`, ok: true }
    : dockerDown
      ? { text: `${imageName}: cannot check while Docker is unavailable.`, ok: false }
      : status.image_state === "unknown"
        ? { text: `${imageName}: could not be checked just now.`, ok: false }
        : {
          text: `${imageName} is not pulled yet; the first run downloads it.`,
          ok: false,
        };

  const keyLine: ExecutorStatusLine | undefined = status.key_env
    ? status.key_set
      ? { text: `${status.key_env} is set.`, ok: true }
      : {
          text: `${status.key_env} is not set in the daemon's environment or the project's .env.`,
          ok: false,
        }
    : undefined;

  // When the daemon's problem IS the missing key, its sentence already says
  // so and how to fix it; a key line saying the same thing above it showed
  // the reason twice.
  const keyIsTheProblem = !!keyLine && !keyLine.ok && !!status.problem && status.problem.includes(status.key_env);
  return {
    imageLine,
    keyLine: keyIsTheProblem ? undefined : keyLine,
    problemLine: status.problem || undefined,
    noteLine: status.note || undefined,
  };
}

// executorReady mirrors the daemon's own definition of ready: an empty
// problem, never derived separately from image/key so the two can't disagree.
export function executorReady(status: ExecutorStatusInput): boolean {
  return !status.problem;
}

// isModelFieldError decides whether a PUT .../executors failure belongs next
// to the Model field specifically. saveExecutor used to route EVERY failure
// there -- a bad image reference, an unknown kind, a transient 5xx -- so an
// error that had nothing to do with the model the user typed showed up
// attached to a field they may not have touched, while the page's general
// error line stayed silently empty. Only executor.ResolveAgentProvider's
// refusals (unknown provider, bad name/type) are actually about the model,
// and its message names it, so that's the signal used here rather than
// re-deriving the daemon's validation rules on the client.
export function isModelFieldError(message: string): boolean {
  return /\bmodel\b/i.test(message);
}

// executorImagePlaceholder is the empty-field hint for the Image input. It
// must never look like a saved value: Settings used to prefill this field
// with the pinned default so it displayed something after every reload that
// was never actually stored (C3), and the placeholder is what replaces that.
export function executorImagePlaceholder(defaultImage: string | undefined): string {
  return defaultImage
    ? `container image — leave blank to use the default (${defaultImage})`
    : "container image (e.g. ghcr.io/org/agent:latest)";
}

// ExecutorResolution is GET .../executors' `resolution`, carried on the row the
// next run would actually use: what harness.yaml asked for (`auto` when the
// kind is unset) and what that resolves to right now.
export interface ExecutorResolution {
  requested: string;
  resolved: string;
  warning?: string;
}

// requestedExecutorKind is the kind harness.yaml asks for. No row is
// `selected` under `auto` (selected means "the configured kind is this row"),
// and Settings used to fall back to its own default, "internal" -- so the page
// said Kind: internal while harness.yaml said `kind: auto`.
export function requestedExecutorKind(
  rows: { name: string; selected: boolean; resolution?: ExecutorResolution }[],
): string {
  const selected = rows.find((r) => r.selected);
  if (selected) return selected.name;
  return rows.find((r) => r.resolution)?.resolution?.requested || "auto";
}

// executorResolutionLine says what will actually run, beside what was asked
// for: "auto → internal" is the case that matters, because it means no real
// code will be written.
export function executorResolutionLine(resolution: ExecutorResolution | undefined): string {
  if (!resolution) return "";
  if (resolution.requested === resolution.resolved) return `Runs: ${resolution.resolved}.`;
  return `Requested: ${resolution.requested} · resolves to: ${resolution.resolved}.`;
}

// hasExecutorStatusLines is false when there is nothing to say -- the internal
// executor needs no image and no key -- so Settings renders no empty panel.
export function hasExecutorStatusLines(lines: ExecutorStatusLines): boolean {
  return !!(lines.imageLine || lines.keyLine || lines.problemLine || lines.noteLine);
}
