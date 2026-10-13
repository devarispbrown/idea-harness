// Pure rendering logic for Settings → Integrations (GET
// /projects/{projectId}/integrations). Kept out of the component so the
// wording -- which is the whole point of a status panel -- is unit-tested.

export interface IntegrationStatus {
  name: string;
  configured: boolean;
  active: boolean;
  tokens?: Record<string, boolean>;
  problems: string[];
  outbox?: { pending: number; delivered: number; dead: number; last_error?: string; last_delivered_at?: string };
  ingest_error?: string;
  socket?: { connected: boolean; last_error?: string; reconnects: number };
  poll?: { last_poll_at?: string; last_error?: string };
  linked_issues?: number;
}

export interface IntegrationsView {
  integrations: IntegrationStatus[];
}

export type IntegrationState = "off" | "ok" | "warn" | "down";

const LABELS: Record<string, string> = { slack: "Slack", linear: "Linear" };

export function integrationLabel(name: string): string {
  return LABELS[name] ?? name;
}

// integrationState is the one-word verdict a row leads with.
export function integrationState(s: IntegrationStatus): IntegrationState {
  if (!s.configured) return "off";
  if (!s.active) return "down";
  if (s.problems.length > 0 || s.ingest_error || (s.socket && !s.socket.connected)) return "warn";
  return "ok";
}

export function integrationHeadline(s: IntegrationStatus): string {
  switch (integrationState(s)) {
    case "off":
      return `Not configured. Add integrations.${s.name} to harness.yaml.`;
    case "down":
      return "Configured but not running.";
    case "warn":
      return "Running, with problems.";
    default:
      return "Running.";
  }
}

// integrationDetails lists the facts under the headline: deliveries, the
// Socket Mode connection, the Linear poll, and which token variables are
// set (never their values -- the API does not return them).
export function integrationDetails(s: IntegrationStatus): string[] {
  if (!s.configured) return [];
  const out: string[] = [];
  if (s.outbox) {
    out.push(`${s.outbox.delivered} delivered · ${s.outbox.pending} pending · ${s.outbox.dead} dropped`);
  }
  if (s.socket) {
    out.push(s.socket.connected ? "Socket Mode connected" : "Socket Mode disconnected");
  }
  if (s.name === "linear" && s.active) {
    out.push(`${s.linked_issues ?? 0} linked issue(s)`);
    if (s.poll?.last_poll_at) out.push(`Last poll ${new Date(s.poll.last_poll_at).toLocaleString()}`);
  }
  for (const [env, set] of Object.entries(s.tokens ?? {}).sort(([a], [b]) => a.localeCompare(b))) {
    out.push(`${env}: ${set ? "set" : "missing"}`);
  }
  return out;
}
