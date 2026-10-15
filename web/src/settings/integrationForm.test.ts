import { describe, expect, it } from "vitest";
import {
  linearConfigFrom,
  linearFormFrom,
  slackConfigFrom,
  slackFormFrom,
  splitList,
  validateLinear,
  validateSlack,
} from "./integrationForm";

const KINDS = ["initiative.created", "initiative.needs_judgment", "run.failed", "initiative.budget_exceeded"];
const DEFAULTS = ["initiative.needs_judgment", "run.failed"];

describe("Slack form", () => {
  it("names a #channel-name, a pasted token and bad user ids at the field", () => {
    const f = slackFormFrom(null, DEFAULTS);
    expect(validateSlack({ ...f, channel: "" }, KINDS).channel).toMatch(/Required/);
    expect(validateSlack({ ...f, channel: "#eng" }, KINDS).channel).toMatch(/channel ID/);
    expect(validateSlack({ ...f, channel: "xoxb-1234-abcd" }, KINDS).channel).toMatch(/token/);
    const e = validateSlack({ ...f, channel: "C0123ABC", allowedUsers: "U012AB, bob", mentionOnJudgment: "here, alice" }, KINDS);
    expect(e.allowedUsers).toMatch(/bob/);
    expect(e.mentionOnJudgment).toMatch(/alice/);
    expect(e.channel).toBeUndefined();
  });

  it("refuses the token itself in a variable-name field", () => {
    const f = { ...slackFormFrom(null, DEFAULTS), channel: "C0123ABC" };
    expect(validateSlack({ ...f, botTokenEnv: "xoxb-123-456" }, KINDS).botTokenEnv).toMatch(/token itself/);
    expect(validateSlack({ ...f, appTokenEnv: "MY APP" }, KINDS).appTokenEnv).toMatch(/variable name/);
    expect(validateSlack({ ...f, botTokenEnv: "TEAM_SLACK_BOT" }, KINDS)).toEqual({});
  });

  it("round-trips a config and leaves defaults out of the written section", () => {
    const cfg = { channel: "C0123ABC", allowed_users: ["U012AB"], mention_on_judgment: ["here"], socket_mode: false };
    const f = slackFormFrom(cfg, DEFAULTS);
    expect(f.events).toEqual(DEFAULTS);
    expect(slackConfigFrom(f, DEFAULTS)).toEqual(cfg);
    const narrowed = slackConfigFrom({ ...f, events: ["run.failed"] }, DEFAULTS);
    expect(narrowed.events).toEqual(["run.failed"]);
  });
});

describe("Linear form", () => {
  it("holds the poll interval to the loader's floor", () => {
    const f = { ...linearFormFrom(null), teamKey: "ENG" };
    expect(validateLinear({ ...f, pollIntervalSeconds: "5" }).pollIntervalSeconds).toMatch(/at least 15/);
    expect(validateLinear({ ...f, pollIntervalSeconds: "30.5" }).pollIntervalSeconds).toBeDefined();
    expect(validateLinear({ ...f, pollIntervalSeconds: "60" })).toEqual({});
    expect(validateLinear({ ...f, teamKey: "" }).teamKey).toMatch(/Required/);
    expect(validateLinear({ ...f, apiKeyEnv: "lin_api_abc" }).apiKeyEnv).toMatch(/token itself/);
  });

  it("writes only what was set, state mapping included", () => {
    const f = linearFormFrom({ team_key: "ENG", state_mapping: { done: "Shipped" }, inbound: false });
    expect(linearConfigFrom({ ...f, pollIntervalSeconds: "120" })).toEqual({
      team_key: "ENG",
      inbound: false,
      poll_interval_seconds: 120,
      state_mapping: { done: "Shipped" },
    });
  });
});

it("splitList takes commas and spaces", () => {
  expect(splitList(" U1, U2  U3,,")).toEqual(["U1", "U2", "U3"]);
});
