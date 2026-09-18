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
}

export interface ExecutorStatusLine {
  text: string;
  ok: boolean;
}

export interface ExecutorStatusLines {
  imageLine: ExecutorStatusLine;
  // Omitted for a keyless executor kind (e.g. one that needs no provider
  // credential at all) rather than shown as a false "missing key".
  keyLine?: ExecutorStatusLine;
  // Omitted when the executor is ready.
  problemLine?: string;
}

export function executorStatusLines(status: ExecutorStatusInput): ExecutorStatusLines {
  const imageName = status.image || "the pinned default image";
  const imageLine: ExecutorStatusLine = status.image_present
    ? { text: `${imageName} is pulled.`, ok: true }
    : {
        text: `${imageName} is not pulled locally — run \`idea executors setup\` to pull it.`,
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

  return {
    imageLine,
    keyLine,
    problemLine: status.problem || undefined,
  };
}

// executorReady mirrors the daemon's own definition of ready: an empty
// problem, never derived separately from image/key so the two can't disagree.
export function executorReady(status: ExecutorStatusInput): boolean {
  return !status.problem;
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
