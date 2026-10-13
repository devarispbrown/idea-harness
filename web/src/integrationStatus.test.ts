import { describe, expect, it } from "vitest";
import {
  integrationDetails,
  integrationHeadline,
  integrationLabel,
  integrationState,
  type IntegrationStatus,
} from "./integrationStatus";

const slackOK: IntegrationStatus = {
  name: "slack",
  configured: true,
  active: true,
  tokens: { SLACK_BOT_TOKEN: true, SLACK_APP_TOKEN: true },
  problems: [],
  outbox: { pending: 0, delivered: 12, dead: 0 },
  socket: { connected: true, reconnects: 0 },
};

describe("integrationState", () => {
  it("is off when not configured", () => {
    const s = { name: "linear", configured: false, active: false, problems: [] };
    expect(integrationState(s)).toBe("off");
    expect(integrationHeadline(s)).toContain("integrations.linear");
    expect(integrationDetails(s)).toEqual([]);
  });

  it("is down when configured but not running (a missing token)", () => {
    expect(integrationState({ ...slackOK, active: false, problems: ["SLACK_BOT_TOKEN is not set"] })).toBe("down");
  });

  it("warns on problems or a dropped socket, even while running", () => {
    expect(integrationState({ ...slackOK, problems: ["allowed_users is empty"] })).toBe("warn");
    expect(integrationState({ ...slackOK, socket: { connected: false, reconnects: 3 } })).toBe("warn");
    expect(integrationState(slackOK)).toBe("ok");
  });
});

describe("integrationDetails", () => {
  it("summarizes deliveries, the socket and which tokens are set", () => {
    const lines = integrationDetails({ ...slackOK, tokens: { SLACK_BOT_TOKEN: true, SLACK_APP_TOKEN: false } });
    expect(lines).toEqual([
      "12 delivered · 0 pending · 0 dropped",
      "Socket Mode connected",
      "SLACK_APP_TOKEN: missing",
      "SLACK_BOT_TOKEN: set",
    ]);
  });

  it("counts linked Linear issues", () => {
    const lines = integrationDetails({ name: "linear", configured: true, active: true, problems: [], linked_issues: 4 });
    expect(lines).toContain("4 linked issue(s)");
  });

  it("labels known integrations", () => {
    expect(integrationLabel("slack")).toBe("Slack");
    expect(integrationLabel("jira")).toBe("jira");
  });
});
