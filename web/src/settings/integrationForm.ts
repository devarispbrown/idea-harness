// Pure form logic for the Slack and Linear editors in Settings →
// Integrations (PUT /projects/{projectId}/integrations/{name}).
//
// The daemon validates every write against the same rules a hand edit of
// harness.yaml meets, so nothing here is the last line of defence. It exists
// so the common mistakes -- a #channel-name where an ID goes, a token pasted
// into the variable-name field, a 5-second poll -- are named next to the
// field before a round trip, in the same words the config loader uses.

export interface SlackConfig {
  channel: string;
  events?: string[];
  mention_on_judgment?: string[];
  allowed_users?: string[];
  socket_mode?: boolean;
  bot_token_env?: string;
  app_token_env?: string;
  ui_base_url?: string;
}

export interface LinearStateMapping {
  todo?: string;
  in_progress?: string;
  in_review?: string;
  done?: string;
  canceled?: string;
}

export interface LinearConfig {
  team_key: string;
  project?: string;
  inbound_label?: string;
  inbound?: boolean;
  poll_interval_seconds?: number;
  state_mapping?: LinearStateMapping;
  api_key_env?: string;
}

// The form keeps lists as the text a person types (comma- or
// space-separated) and numbers as strings, so a half-typed value is never
// silently coerced.
export interface SlackForm {
  channel: string;
  events: string[];
  mentionOnJudgment: string;
  allowedUsers: string;
  socketMode: boolean;
  botTokenEnv: string;
  appTokenEnv: string;
  uiBaseURL: string;
}

export interface LinearForm {
  teamKey: string;
  project: string;
  inboundLabel: string;
  inbound: boolean;
  pollIntervalSeconds: string;
  states: Required<LinearStateMapping>;
  apiKeyEnv: string;
}

export type FieldErrors = Record<string, string>;

export const MIN_LINEAR_POLL_SECONDS = 15;

const SLACK_USER_RE = /^[UW][A-Z0-9]{2,}$/;
const SLACK_CHANNEL_RE = /^[CGD][A-Z0-9]{2,}$/;
const ENV_NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;
// The prefixes of the tokens these integrations use, plus the generic shapes
// a pasted secret tends to have. The daemon runs its full credential catalog
// on every value; this is only the early, local warning.
const TOKEN_LIKE_RE = /^(xox[abpors]-|xapp-|lin_api_|sk-|ghp_|gho_|github_pat_)/i;

// splitList turns "U1, U2  U3" into ["U1", "U2", "U3"].
export function splitList(s: string): string[] {
  return s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter(Boolean);
}

export function slackFormFrom(c: SlackConfig | null | undefined, defaultEvents: string[]): SlackForm {
  return {
    channel: c?.channel ?? "",
    events: c?.events && c.events.length > 0 ? [...c.events] : [...defaultEvents],
    mentionOnJudgment: (c?.mention_on_judgment ?? []).join(", "),
    allowedUsers: (c?.allowed_users ?? []).join(", "),
    socketMode: c?.socket_mode ?? true,
    botTokenEnv: c?.bot_token_env ?? "",
    appTokenEnv: c?.app_token_env ?? "",
    uiBaseURL: c?.ui_base_url ?? "",
  };
}

export function linearFormFrom(c: LinearConfig | null | undefined): LinearForm {
  const m = c?.state_mapping ?? {};
  return {
    teamKey: c?.team_key ?? "",
    project: c?.project ?? "",
    inboundLabel: c?.inbound_label ?? "",
    inbound: c?.inbound ?? true,
    pollIntervalSeconds: c?.poll_interval_seconds ? String(c.poll_interval_seconds) : "",
    states: {
      todo: m.todo ?? "",
      in_progress: m.in_progress ?? "",
      in_review: m.in_review ?? "",
      done: m.done ?? "",
      canceled: m.canceled ?? "",
    },
    apiKeyEnv: c?.api_key_env ?? "",
  };
}

function envNameError(v: string): string {
  if (v === "") return "";
  if (TOKEN_LIKE_RE.test(v)) return "This looks like the token itself. Put the token in the environment or .env, and its variable NAME here.";
  if (!ENV_NAME_RE.test(v)) return "Must be an environment variable name (letters, digits, _).";
  return "";
}

function tokenPasted(v: string): boolean {
  return TOKEN_LIKE_RE.test(v.trim());
}

export function validateSlack(f: SlackForm, knownEvents: string[]): FieldErrors {
  const e: FieldErrors = {};
  const ch = f.channel.trim();
  if (!ch) e.channel = "Required: the channel ID (C0123…), not its #name.";
  else if (tokenPasted(ch)) e.channel = "This looks like a token, not a channel ID.";
  else if (ch.startsWith("#")) e.channel = "Use the channel ID (C0123…): slash commands are routed back by ID, and a #name never matches it.";
  else if (!SLACK_CHANNEL_RE.test(ch)) e.channel = "A Slack channel ID looks like C0123ABCD.";
  const unknown = f.events.filter((ev) => !knownEvents.includes(ev));
  if (unknown.length > 0) e.events = `Unknown event kind: ${unknown.join(", ")}.`;
  if (f.events.length === 0) e.events = "Pick at least one event (or reset to the defaults).";
  const badMention = splitList(f.mentionOnJudgment).filter((m) => m !== "here" && m !== "channel" && !SLACK_USER_RE.test(m));
  if (badMention.length > 0) e.mentionOnJudgment = `Not a Slack user ID, "here" or "channel": ${badMention.join(", ")}.`;
  const badUsers = splitList(f.allowedUsers).filter((u) => !SLACK_USER_RE.test(u));
  if (badUsers.length > 0) e.allowedUsers = `Not a Slack user ID (U… or W…): ${badUsers.join(", ")}.`;
  const bot = envNameError(f.botTokenEnv.trim());
  if (bot) e.botTokenEnv = bot;
  const app = envNameError(f.appTokenEnv.trim());
  if (app) e.appTokenEnv = app;
  const ui = f.uiBaseURL.trim();
  if (ui && !/^https?:\/\/[^\s/@]+/i.test(ui)) e.uiBaseURL = "An absolute http(s) URL, e.g. http://127.0.0.1:8787.";
  return e;
}

export function validateLinear(f: LinearForm): FieldErrors {
  const e: FieldErrors = {};
  const team = f.teamKey.trim();
  if (!team) e.teamKey = 'Required: the team key (the "ENG" in ENG-123).';
  else if (tokenPasted(team)) e.teamKey = "This looks like a token, not a team key.";
  const poll = f.pollIntervalSeconds.trim();
  if (poll !== "") {
    const n = Number(poll);
    if (!Number.isInteger(n) || n < MIN_LINEAR_POLL_SECONDS) {
      e.pollIntervalSeconds = `Whole seconds, at least ${MIN_LINEAR_POLL_SECONDS} (Linear rate-limits per key).`;
    }
  }
  for (const [k, v] of Object.entries(f.states)) {
    if (tokenPasted(v)) e[`state_${k}`] = "This looks like a token, not a state name.";
  }
  if (tokenPasted(f.project)) e.project = "This looks like a token, not a project name.";
  const key = envNameError(f.apiKeyEnv.trim());
  if (key) e.apiKeyEnv = key;
  return e;
}

// slackConfigFrom builds the section PUT sends. Empty optional fields are
// left out, so harness.yaml keeps showing only what was chosen; events equal
// to the defaults are left out too, so the file says "the defaults" rather
// than freezing today's list.
export function slackConfigFrom(f: SlackForm, defaultEvents: string[]): SlackConfig {
  const out: SlackConfig = { channel: f.channel.trim() };
  const sameAsDefault =
    f.events.length === defaultEvents.length && defaultEvents.every((ev) => f.events.includes(ev));
  if (!sameAsDefault) out.events = [...f.events];
  const mention = splitList(f.mentionOnJudgment);
  if (mention.length) out.mention_on_judgment = mention;
  const users = splitList(f.allowedUsers);
  if (users.length) out.allowed_users = users;
  if (!f.socketMode) out.socket_mode = false;
  if (f.botTokenEnv.trim()) out.bot_token_env = f.botTokenEnv.trim();
  if (f.appTokenEnv.trim()) out.app_token_env = f.appTokenEnv.trim();
  if (f.uiBaseURL.trim()) out.ui_base_url = f.uiBaseURL.trim();
  return out;
}

export function linearConfigFrom(f: LinearForm): LinearConfig {
  const out: LinearConfig = { team_key: f.teamKey.trim() };
  if (f.project.trim()) out.project = f.project.trim();
  if (f.inboundLabel.trim()) out.inbound_label = f.inboundLabel.trim();
  if (!f.inbound) out.inbound = false;
  if (f.pollIntervalSeconds.trim()) out.poll_interval_seconds = Number(f.pollIntervalSeconds.trim());
  const states: LinearStateMapping = {};
  for (const [k, v] of Object.entries(f.states) as [keyof LinearStateMapping, string][]) {
    if (v.trim()) states[k] = v.trim();
  }
  if (Object.keys(states).length) out.state_mapping = states;
  if (f.apiKeyEnv.trim()) out.api_key_env = f.apiKeyEnv.trim();
  return out;
}

export function hasErrors(e: FieldErrors): boolean {
  return Object.keys(e).length > 0;
}
