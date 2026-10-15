// Pure logic for the Settings → Executor profile editor: the form's shape,
// what each kind trusts, the checks worth making before a round trip, and
// the PUT /executors body (only the fields that changed).
//
// The daemon is the authority -- it refuses an unsafe OpenHands URL, a
// secret-looking MCP literal, an acp kind without an image and command --
// and this mirrors those rules so the reason shows next to the field.

export const EXECUTOR_KINDS = ["auto", "internal", "container", "omp", "hermes", "acp", "openhands"] as const;
export type ExecutorKindName = (typeof EXECUTOR_KINDS)[number];

export interface NameValue {
  name: string;
  value?: string;
  value_env?: string;
}

export interface MCPServer {
  name: string;
  type?: "stdio" | "http" | "sse" | "";
  command?: string;
  args?: string[];
  env?: NameValue[];
  url?: string;
  headers?: NameValue[];
}

export interface ExecutorConfig {
  kind: string;
  image?: string;
  model?: string;
  acp?: { command?: string[]; key_env?: string; timeout_seconds?: number };
  openhands?: {
    url?: string;
    api_key_env?: string;
    agent?: string;
    acp_command?: string[];
    workspace_dir?: string;
    withhold_provider_key?: boolean;
    max_iterations?: number;
    timeout_seconds?: number;
    ui_url?: string;
  };
  mcp_servers?: MCPServer[];
}

export interface ExecutorConfigView {
  config: ExecutorConfig;
  compat: { kind: string; model: string; blocked: boolean; message: string } | null;
  env: Record<string, boolean>;
  problems: { severity: string; field: string; message: string }[];
}

// One MCP entry as edited: env/headers rows carry a mode, so the form can
// switch a row between "a literal" and "a variable's name" without losing it.
export interface NameValueRow {
  name: string;
  mode: "value" | "value_env";
  text: string;
}

export interface MCPServerForm {
  name: string;
  type: "stdio" | "http" | "sse";
  command: string;
  args: string;
  url: string;
  pairs: NameValueRow[]; // env for stdio, headers for http/sse
}

export interface ExecutorForm {
  kind: string;
  image: string;
  model: string;
  acpCommand: string;
  acpKeyEnv: string;
  acpTimeoutSeconds: string;
  ohURL: string;
  ohAPIKeyEnv: string;
  ohAgent: string;
  ohACPCommand: string;
  ohWithholdKey: boolean;
  ohUIURL: string;
  mcp: MCPServerForm[];
}

export type FieldErrors = Record<string, string>;

// TRUST is the one-paragraph trade-off per kind, condensed from
// docs/agents.md ("Security / trust tradeoff") and docs/security-model.md.
export const TRUST: Record<string, { level: "none" | "local" | "remote"; text: string }> = {
  auto: {
    level: "local",
    text:
      "Resolves per run: hermes (network egress, your provider key, auto-approve) when Docker is reachable and the coding model's key is set, otherwise the internal placeholder, which writes no real code.",
  },
  internal: {
    level: "none",
    text: "No network, no key, no container: writes a placeholder file, not code. Nothing leaves this machine.",
  },
  container: {
    level: "local",
    text: "Runs in a hardened container (read-only root, all capabilities dropped, no new privileges) with no model and no key.",
  },
  omp: {
    level: "local",
    text:
      "Runs in a hardened container (read-only root, capabilities dropped) but with network egress, its provider API key, and auto-approve: it writes files and runs commands without asking. A malicious image or a prompt injection could exfiltrate the key. Only use an image you trust.",
  },
  hermes: {
    level: "local",
    text:
      "Runs in a hardened container (read-only root, capabilities dropped) but with network egress, its provider API key, and --yolo auto-approve: it writes files and runs commands without asking. A malicious image or a prompt injection could exfiltrate the key. Only use an image you trust.",
  },
  acp: {
    level: "local",
    text:
      "Any Agent Client Protocol agent in the same hardened container as hermes/omp: network egress, the provider key, every permission answered allow_once. MCP servers below widen its reach: a stdio server runs inside the container with the agent; an http/sse server receives whatever its headers carry.",
  },
  openhands: {
    level: "remote",
    text:
      "Runs on an OpenHands Agent Server, usually another machine: the work root is uploaded there, and the provider key too unless withheld. The harness does not enforce that machine's sandbox. Use only a server you operate or trust, over https.",
  },
};

const ENV_NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;
// Names that carry a credential, as the config loader's secret-key rule
// matches them (config.secretKeyRe), plus Authorization.
const SECRET_NAME_RE = /(secret|password|passwd|api_?key|token|credential|private_?key|bearer)/i;
const SECRET_VALUE_RE = /^(xox[abpors]-|xapp-|lin_api_|sk-|sk_live_|ghp_|gho_|github_pat_|glpat-|AKIA|Bearer\s)/i;

// argvText renders an argv for one text field; argvFrom parses it back.
// Whitespace-separated words, or a JSON array when an argument has a space
// -- the same rule `idea executors setup --acp-command` takes.
export function argvText(argv: string[] | undefined): string {
  if (!argv || argv.length === 0) return "";
  if (argv.some((a) => /\s/.test(a) || a === "")) return JSON.stringify(argv);
  return argv.join(" ");
}

export function argvFrom(text: string): string[] | null {
  const t = text.trim();
  if (t === "") return [];
  if (t.startsWith("[")) {
    try {
      const v: unknown = JSON.parse(t);
      if (Array.isArray(v) && v.every((x) => typeof x === "string")) return v as string[];
    } catch {
      /* fall through */
    }
    return null;
  }
  return t.split(/\s+/);
}

function rowsFrom(list: NameValue[] | undefined): NameValueRow[] {
  return (list ?? []).map((nv) =>
    nv.value_env !== undefined && nv.value_env !== ""
      ? { name: nv.name, mode: "value_env", text: nv.value_env }
      : { name: nv.name, mode: "value", text: nv.value ?? "" },
  );
}

export function executorFormFrom(c: ExecutorConfig | null | undefined): ExecutorForm {
  return {
    kind: c?.kind || "auto",
    image: c?.image ?? "",
    model: c?.model ?? "",
    acpCommand: argvText(c?.acp?.command),
    acpKeyEnv: c?.acp?.key_env ?? "",
    acpTimeoutSeconds: c?.acp?.timeout_seconds ? String(c.acp.timeout_seconds) : "",
    ohURL: c?.openhands?.url ?? "",
    ohAPIKeyEnv: c?.openhands?.api_key_env ?? "",
    ohAgent: c?.openhands?.agent ?? "",
    ohACPCommand: argvText(c?.openhands?.acp_command),
    ohWithholdKey: c?.openhands?.withhold_provider_key ?? false,
    ohUIURL: c?.openhands?.ui_url ?? "",
    mcp: (c?.mcp_servers ?? []).map((m) => {
      const type = (m.type || "stdio") as MCPServerForm["type"];
      return {
        name: m.name,
        type,
        command: m.command ?? "",
        args: argvText(m.args),
        url: m.url ?? "",
        pairs: rowsFrom(type === "stdio" ? m.env : m.headers),
      };
    }),
  };
}

export function emptyMCPServer(): MCPServerForm {
  return { name: "", type: "stdio", command: "", args: "", url: "", pairs: [] };
}

function isLoopbackHost(host: string): boolean {
  const h = host.replace(/^\[|\]$/g, "").toLowerCase();
  return h === "localhost" || h === "::1" || /^127\./.test(h);
}

// openHandsURLError mirrors config.ValidateOpenHandsURL.
export function openHandsURLError(raw: string): string {
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return "Not a URL.";
  }
  if (u.protocol !== "https:" && u.protocol !== "http:") return "Must be an http(s) URL.";
  if (u.username || u.password) return "Must not carry credentials; name the session key's variable instead.";
  if (u.search || u.hash) return "A base URL, without a query or fragment.";
  if (u.protocol === "http:" && !isLoopbackHost(u.hostname)) {
    return "Plain http to another machine would carry the session key, the work root and the provider key in the clear. Use https, or an SSH tunnel to localhost.";
  }
  return "";
}

// pairError is the MCP literal rule from config.validateNameValue.
export function pairError(row: NameValueRow): string {
  if (!row.name.trim()) return "Needs a name.";
  if (row.mode === "value_env") {
    if (!ENV_NAME_RE.test(row.text.trim())) return "Must be an environment variable name.";
    return "";
  }
  if (row.text === "") return "";
  if (SECRET_NAME_RE.test(row.name) || /^authorization$/i.test(row.name)) {
    return `${row.name} carries a credential: switch it to "from variable" and name the variable that holds it.`;
  }
  if (SECRET_VALUE_RE.test(row.text.trim())) return "This looks like a credential: name the variable that holds it instead.";
  return "";
}

export function validateExecutor(f: ExecutorForm): FieldErrors {
  const e: FieldErrors = {};
  if (/\s/.test(f.image)) e.image = "An image reference has no whitespace.";
  if (f.kind === "acp") {
    if (!f.image.trim()) e.image = "acp needs an image: there is no canonical ACP image.";
    const argv = argvFrom(f.acpCommand);
    if (argv === null) e.acpCommand = "Words separated by spaces, or a JSON array of strings.";
    else if (argv.length === 0) e.acpCommand = 'acp needs the agent\'s argv inside the image, e.g. claude-code-acp or ["gemini","--experimental-acp"].';
  }
  if (f.acpKeyEnv && !ENV_NAME_RE.test(f.acpKeyEnv)) e.acpKeyEnv = "Must be an environment variable name.";
  if (f.acpTimeoutSeconds.trim() !== "") {
    const n = Number(f.acpTimeoutSeconds);
    if (!Number.isInteger(n) || n < 0) e.acpTimeoutSeconds = "Whole seconds, 0 or more (0: the default, 840).";
  }
  if (f.kind === "openhands") {
    if (!f.ohURL.trim()) e.ohURL = "openhands needs the Agent Server's URL.";
    if (f.ohAgent === "acp" && (argvFrom(f.ohACPCommand) ?? []).length === 0) {
      e.ohACPCommand = "agent: acp needs the ACP agent's argv on the server, e.g. claude-agent-acp.";
    }
  }
  if (f.ohURL.trim()) {
    const msg = openHandsURLError(f.ohURL.trim());
    if (msg) e.ohURL = msg;
  }
  if (f.ohUIURL.trim()) {
    try {
      const u = new URL(f.ohUIURL.trim());
      if ((u.protocol !== "https:" && u.protocol !== "http:") || u.username || u.password) throw new Error("bad");
    } catch {
      e.ohUIURL = "An absolute http(s) URL without credentials.";
    }
  }
  if (f.ohAPIKeyEnv && !ENV_NAME_RE.test(f.ohAPIKeyEnv)) {
    e.ohAPIKeyEnv = "The NAME of the variable holding the session key, never the key itself.";
  }
  const seen = new Set<string>();
  f.mcp.forEach((m, i) => {
    const k = `mcp.${i}`;
    if (!m.name.trim()) e[`${k}.name`] = "Needs a name.";
    else if (seen.has(m.name.trim())) e[`${k}.name`] = `Duplicate MCP server name "${m.name.trim()}".`;
    seen.add(m.name.trim());
    if (m.type === "stdio") {
      if (!m.command.trim()) e[`${k}.command`] = "A stdio server needs a command (it runs inside the agent's container).";
      if (argvFrom(m.args) === null) e[`${k}.args`] = "Words separated by spaces, or a JSON array of strings.";
    } else if (!/^https?:\/\//i.test(m.url.trim())) {
      e[`${k}.url`] = `An ${m.type} server needs its http(s) URL.`;
    }
    m.pairs.forEach((p, j) => {
      const msg = pairError(p);
      if (msg) e[`${k}.pair.${j}`] = msg;
    });
  });
  return e;
}

function pairsOut(rows: NameValueRow[]): NameValue[] {
  return rows.map((r) =>
    r.mode === "value_env" ? { name: r.name.trim(), value_env: r.text.trim() } : { name: r.name.trim(), value: r.text },
  );
}

export function mcpServersFrom(forms: MCPServerForm[]): MCPServer[] {
  return forms.map((m) => {
    if (m.type === "stdio") {
      const out: MCPServer = { name: m.name.trim(), command: m.command.trim() };
      const args = argvFrom(m.args) ?? [];
      if (args.length) out.args = args;
      if (m.pairs.length) out.env = pairsOut(m.pairs);
      return out;
    }
    const out: MCPServer = { name: m.name.trim(), type: m.type, url: m.url.trim() };
    if (m.pairs.length) out.headers = pairsOut(m.pairs);
    return out;
  });
}

// executorPutBody is the PUT /executors body: only the fields that differ
// from what was loaded, so a save never rewrites a field the person did not
// touch (and a hand edit made since is not clobbered field by field either --
// the daemon merges onto what is on disk).
export function executorPutBody(initial: ExecutorForm, f: ExecutorForm): Record<string, unknown> {
  const b: Record<string, unknown> = {};
  if (f.kind !== initial.kind) b.kind = f.kind;
  if (f.image !== initial.image) b.image = f.image.trim();
  if (f.model !== initial.model) b.model = f.model.trim();
  if (f.acpCommand !== initial.acpCommand) b.acp_command = argvFrom(f.acpCommand) ?? [];
  if (f.acpKeyEnv !== initial.acpKeyEnv) b.acp_key_env = f.acpKeyEnv.trim();
  if (f.acpTimeoutSeconds !== initial.acpTimeoutSeconds) b.acp_timeout_seconds = Number(f.acpTimeoutSeconds || "0");
  if (f.ohURL !== initial.ohURL) b.openhands_url = f.ohURL.trim();
  if (f.ohAPIKeyEnv !== initial.ohAPIKeyEnv) b.openhands_api_key_env = f.ohAPIKeyEnv.trim();
  if (f.ohAgent !== initial.ohAgent) b.openhands_agent = f.ohAgent;
  if (f.ohACPCommand !== initial.ohACPCommand) b.openhands_acp_command = argvFrom(f.ohACPCommand) ?? [];
  if (f.ohWithholdKey !== initial.ohWithholdKey) b.openhands_withhold_provider_key = f.ohWithholdKey;
  if (f.ohUIURL !== initial.ohUIURL) b.openhands_ui_url = f.ohUIURL.trim();
  if (JSON.stringify(mcpServersFrom(f.mcp)) !== JSON.stringify(mcpServersFrom(initial.mcp))) {
    b.mcp_servers = mcpServersFrom(f.mcp);
  }
  return b;
}

// usesImage / usesModel decide which fields the form shows for a kind.
export function usesImage(kind: string): boolean {
  return kind !== "internal" && kind !== "openhands";
}

export function usesModel(kind: string): boolean {
  return kind === "auto" || kind === "omp" || kind === "hermes" || kind === "acp" || kind === "openhands";
}
