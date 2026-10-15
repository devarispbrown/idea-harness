import { describe, expect, it } from "vitest";
import {
  argvFrom,
  argvText,
  emptyMCPServer,
  executorFormFrom,
  executorPutBody,
  openHandsURLError,
  pairError,
  TRUST,
  EXECUTOR_KINDS,
  validateExecutor,
} from "./executorForm";

describe("argv fields", () => {
  it("round-trips words and JSON arrays", () => {
    expect(argvFrom("gemini --experimental-acp")).toEqual(["gemini", "--experimental-acp"]);
    expect(argvFrom('["sh", "-c", "run it"]')).toEqual(["sh", "-c", "run it"]);
    expect(argvFrom("[not json")).toBeNull();
    expect(argvText(["sh", "-c", "run it"])).toBe('["sh","-c","run it"]');
    expect(argvText(["claude-code-acp"])).toBe("claude-code-acp");
  });
});

describe("executor validation", () => {
  it("names what acp and openhands need", () => {
    const f = executorFormFrom({ kind: "acp" });
    const e = validateExecutor(f);
    expect(e.image).toMatch(/no canonical ACP image/);
    expect(e.acpCommand).toMatch(/argv/);
    const oh = validateExecutor({ ...executorFormFrom({ kind: "openhands" }), ohAgent: "acp" });
    expect(oh.ohURL).toMatch(/Agent Server/);
    expect(oh.ohACPCommand).toMatch(/argv on the server/);
  });

  it("refuses plain http to another machine, allows a tunnel", () => {
    expect(openHandsURLError("http://agents.example.com")).toMatch(/https/);
    expect(openHandsURLError("http://127.0.0.1:8000")).toBe("");
    expect(openHandsURLError("https://u:p@agents.example.com")).toMatch(/credentials/);
    expect(openHandsURLError("https://agents.example.com")).toBe("");
  });

  it("refuses a secret-looking MCP literal, as the config loader does", () => {
    expect(pairError({ name: "GITHUB_TOKEN", mode: "value", text: "abc" })).toMatch(/from variable/);
    expect(pairError({ name: "Authorization", mode: "value", text: "Bearer x" })).toMatch(/credential/);
    expect(pairError({ name: "LOG_LEVEL", mode: "value", text: "ghp_0123456789" })).toMatch(/looks like a credential/);
    expect(pairError({ name: "LOG_LEVEL", mode: "value", text: "debug" })).toBe("");
    expect(pairError({ name: "GITHUB_TOKEN", mode: "value_env", text: "GITHUB_TOKEN" })).toBe("");
    expect(pairError({ name: "X", mode: "value_env", text: "not a name" })).toMatch(/variable name/);
  });

  it("checks MCP servers by transport and refuses duplicates", () => {
    const f = executorFormFrom({ kind: "acp", image: "img", acp: { command: ["a"] } });
    const http = { ...emptyMCPServer(), name: "docs", type: "http" as const };
    const e = validateExecutor({ ...f, mcp: [{ ...emptyMCPServer(), name: "gh" }, { ...emptyMCPServer(), name: "gh", command: "x" }, http] });
    expect(e["mcp.0.command"]).toMatch(/stdio/);
    expect(e["mcp.1.name"]).toMatch(/Duplicate/);
    expect(e["mcp.2.url"]).toMatch(/http/);
  });
});

describe("the PUT body", () => {
  it("sends only what changed", () => {
    const loaded = executorFormFrom({ kind: "hermes", image: "img:1", model: "openai/gpt-5" });
    expect(executorPutBody(loaded, loaded)).toEqual({});
    expect(executorPutBody(loaded, { ...loaded, kind: "internal" })).toEqual({ kind: "internal" });
  });

  it("sends MCP servers whole, env rows as value or value_env", () => {
    const loaded = executorFormFrom({ kind: "acp", image: "i", acp: { command: ["a"] } });
    const edited = {
      ...loaded,
      mcp: [
        {
          ...emptyMCPServer(),
          name: "gh",
          command: "github-mcp-server",
          args: "stdio",
          pairs: [
            { name: "GITHUB_TOKEN", mode: "value_env" as const, text: "GITHUB_TOKEN" },
            { name: "LOG_LEVEL", mode: "value" as const, text: "info" },
          ],
        },
      ],
    };
    expect(executorPutBody(loaded, edited)).toEqual({
      mcp_servers: [
        {
          name: "gh",
          command: "github-mcp-server",
          args: ["stdio"],
          env: [
            { name: "GITHUB_TOKEN", value_env: "GITHUB_TOKEN" },
            { name: "LOG_LEVEL", value: "info" },
          ],
        },
      ],
    });
  });
});

it("states a trust trade-off for every kind", () => {
  for (const k of EXECUTOR_KINDS) expect(TRUST[k]?.text.length).toBeGreaterThan(20);
  expect(TRUST.openhands.level).toBe("remote");
  expect(TRUST.internal.level).toBe("none");
});
