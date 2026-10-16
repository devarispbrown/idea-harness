// @vitest-environment jsdom
//
// Component tests for the Phase 4 settings editors: what they refuse before
// a round trip, what they send when they do send, what they render from the
// daemon, and what they show when a write is refused.
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import IntegrationEditor from "./IntegrationEditor";
import ExecutorSettings from "./ExecutorSettings";
import BudgetSettings from "./BudgetSettings";
import BudgetMeter from "../BudgetMeter";
import IntegrationsPanel from "../IntegrationsPanel";
import AutomationsPage from "../AutomationsPage";
import { fail, fakeDaemon } from "../testing/fakeDaemon";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const P = "/api/v1/projects/p1";

describe("IntegrationEditor", () => {
  it("refuses a token pasted as the channel without sending it anywhere", async () => {
    const { requests } = fakeDaemon({ [`PUT ${P}/integrations/slack`]: { status: "updated" } });
    const onSaved = vi.fn();
    render(
      <IntegrationEditor
        projectId="p1"
        name="slack"
        config={null}
        tokens={{ SLACK_BOT_TOKEN: false }}
        eventKinds={["run.failed"]}
        defaultEvents={["run.failed"]}
        onSaved={onSaved}
      />,
    );
    fireEvent.change(screen.getByLabelText("Channel ID"), { target: { value: "xoxb-1234-5678-abcdef" } });
    fireEvent.click(screen.getByRole("button", { name: "Turn on Slack" }));
    expect(await screen.findByText(/looks like a token/)).toBeTruthy();
    expect(requests.filter((r) => r.method === "PUT")).toHaveLength(0);
    expect(screen.getByText(/is not set/)).toBeTruthy();

    fireEvent.change(screen.getByLabelText("Channel ID"), { target: { value: "C0123ABC" } });
    fireEvent.click(screen.getByRole("button", { name: "Turn on Slack" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(requests.find((r) => r.method === "PUT")?.body).toEqual({ config: { channel: "C0123ABC" } });
  });
});

describe("BudgetMeter", () => {
  const budget = {
    cap_usd: 0.15,
    source: "project",
    initiative_cap_usd: null,
    project_default_usd: 0.15,
    spent_usd: 0.2,
    model_usd: 0.2,
    agent_usd: null,
    agent_runs: 1,
    agent_not_counted: true,
    remaining_usd: 0,
    exceeded: true,
    parked: true,
    note: "",
  };

  it("in the shell, says the run stopped and links to raise the cap", async () => {
    fakeDaemon({ [`GET ${P}/initiatives/i1/budget`]: budget });
    render(<BudgetMeter projectId="p1" initiativeId="i1" variant="shell" />);
    expect(await screen.findByText(/Stopped at its spending cap/)).toBeTruthy();
    expect(screen.getByText(/agent spend not counted/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "Raise the cap" }).getAttribute("href")).toBe("#/i/i1/build");
  });

  it("on Build, raises the cap and reports the initiative resumed", async () => {
    const { requests } = fakeDaemon({
      [`GET ${P}/initiatives/i1/budget`]: budget,
      [`PUT ${P}/initiatives/i1/budget`]: { budget: { ...budget, cap_usd: 10, source: "initiative", exceeded: false, parked: false }, resumed: true },
    });
    render(<BudgetMeter projectId="p1" initiativeId="i1" variant="build" />);
    const input = await screen.findByLabelText("This initiative's cap (USD)");
    fireEvent.change(input, { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Raise cap" }));
    expect(await screen.findByText(/back where it stopped/)).toBeTruthy();
    expect(requests.find((r) => r.method === "PUT")?.body).toEqual({ cap_usd: 10 });
  });

  it("on Build, refuses a cap of $0 without sending it, and shows a refused write", async () => {
    const { requests } = fakeDaemon({
      [`GET ${P}/initiatives/i1/budget`]: { ...budget, parked: false, exceeded: false },
      [`PUT ${P}/initiatives/i1/budget`]: fail(409, "the initiative is running"),
    });
    render(<BudgetMeter projectId="p1" initiativeId="i1" variant="build" />);
    const input = await screen.findByLabelText("This initiative's cap (USD)");
    fireEvent.change(input, { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Set cap" }));
    expect(await screen.findByText(/Must be more than \$0/)).toBeTruthy();
    expect(requests.filter((r) => r.method === "PUT")).toHaveLength(0);

    fireEvent.change(input, { target: { value: "abc" } });
    fireEvent.click(screen.getByRole("button", { name: "Set cap" }));
    expect(await screen.findByText(/A dollar amount/)).toBeTruthy();
    expect(requests.filter((r) => r.method === "PUT")).toHaveLength(0);

    fireEvent.change(input, { target: { value: "$3" } });
    fireEvent.click(screen.getByRole("button", { name: "Set cap" }));
    expect(await screen.findByText("the initiative is running")).toBeTruthy();
    expect(requests.find((r) => r.method === "PUT")?.body).toEqual({ cap_usd: 3 });
  });
});

describe("IntegrationEditor (Linear)", () => {
  it("refuses a poll interval under 15s and a pasted API key, then sends names and values only", async () => {
    const { requests } = fakeDaemon({ [`PUT ${P}/integrations/linear`]: { status: "updated" } });
    const onSaved = vi.fn();
    render(
      <IntegrationEditor projectId="p1" name="linear" config={null} tokens={{ LINEAR_API_KEY: true }} eventKinds={[]} defaultEvents={[]} onSaved={onSaved} />,
    );
    expect(screen.getByText(/is set/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Team key"), { target: { value: "ENG" } });
    fireEvent.change(screen.getByLabelText("Poll interval (seconds, at least 15)"), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText("API key variable"), { target: { value: "lin_api_abcdef123" } });
    fireEvent.click(screen.getByRole("button", { name: "Turn on Linear" }));
    expect(await screen.findByText(/at least 15/, { selector: "p.error" })).toBeTruthy();
    expect(screen.getByText(/looks like the token itself/)).toBeTruthy();
    // The error is the field's description, for a screen reader.
    const key = screen.getByLabelText("API key variable");
    expect(key.getAttribute("aria-invalid")).toBe("true");
    expect(document.getElementById(key.getAttribute("aria-describedby")!)!.textContent).toMatch(/token itself/);
    expect(requests.filter((r) => r.method === "PUT")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Poll interval (seconds, at least 15)"), { target: { value: "120" } });
    fireEvent.change(key, { target: { value: "MY_LINEAR_KEY" } });
    fireEvent.click(screen.getByRole("button", { name: "Turn on Linear" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith("Linear settings saved to harness.yaml."));
    const body = requests.find((r) => r.method === "PUT")!.body as { config: Record<string, unknown> };
    expect(body.config).toMatchObject({ team_key: "ENG", poll_interval_seconds: 120, api_key_env: "MY_LINEAR_KEY" });
  });

  it("shows the daemon's refusal and keeps the form open; Turn off sends a null config", async () => {
    const { requests } = fakeDaemon({ [`PUT ${P}/integrations/linear`]: fail(422, "integrations.linear: team_key ENG is unknown") });
    const onSaved = vi.fn();
    render(
      <IntegrationEditor
        projectId="p1"
        name="linear"
        config={{ team_key: "ENG" }}
        tokens={{ LINEAR_API_KEY: false }}
        eventKinds={[]}
        defaultEvents={[]}
        onSaved={onSaved}
      />,
    );
    expect((screen.getByLabelText("Team key") as HTMLInputElement).value).toBe("ENG");
    fireEvent.click(screen.getByRole("button", { name: "Save Linear settings" }));
    expect(await screen.findByText("integrations.linear: team_key ENG is unknown")).toBeTruthy();
    expect(onSaved).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Turn off" }));
    await waitFor(() => expect(requests.filter((r) => r.method === "PUT")).toHaveLength(2));
    expect(requests.filter((r) => r.method === "PUT")[1].body).toEqual({ config: null });
  });
});

describe("IntegrationsPanel", () => {
  const view = {
    integrations: [
      { name: "slack", configured: true, active: true, problems: [], tokens: { SLACK_BOT_TOKEN: true }, config: { channel: "C0123ABC" }, token_envs: ["SLACK_BOT_TOKEN"] },
      { name: "linear", configured: false, active: false, problems: [], config: null, token_envs: ["LINEAR_API_KEY"] },
    ],
    event_kinds: ["run.failed"],
    default_slack_events: ["run.failed"],
  };

  it("lists each integration, sends a test, and opens the editor with the variable's state", async () => {
    const { requests } = fakeDaemon({
      [`GET ${P}/integrations`]: view,
      [`POST ${P}/integrations/slack/test`]: { ok: false, error: "channel_not_found" },
    });
    render(<IntegrationsPanel projectId="p1" />);
    const slack = await screen.findByText("Slack");
    const row = slack.closest("li")!;
    fireEvent.click(within(row as HTMLElement).getByRole("button", { name: "Send test message" }));
    expect(await screen.findByText("channel_not_found")).toBeTruthy();
    expect(requests.some((r) => r.method === "POST" && r.path === `${P}/integrations/slack/test`)).toBe(true);

    const setup = screen.getByRole("button", { name: "Set up Linear" });
    expect(setup.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(setup);
    expect(screen.getByRole("button", { name: "Close" }).getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByText(/LINEAR_API_KEY/, { selector: "code" }).parentElement!.textContent).toMatch(/is not set/);
  });

  it("says why when the status cannot be read", async () => {
    fakeDaemon({ [`GET ${P}/integrations`]: fail(500, "harness.yaml: line 3: bad indentation") });
    render(<IntegrationsPanel projectId="p1" />);
    expect(await screen.findByText("harness.yaml: line 3: bad indentation")).toBeTruthy();
  });
});

describe("ExecutorSettings", () => {
  const config = (c: Record<string, unknown>, extra: Record<string, unknown> = {}) => ({
    config: c,
    compat: null,
    env: { OPENHANDS_API_KEY: false, GH_TOKEN: true },
    problems: [],
    ...extra,
  });
  const rows = [
    { name: "hermes", trust: "local_external", sandbox_mode: "container", network: "egress", capabilities: [], selected: true, image: "", model: "", default_image: "nousresearch/hermes-agent:v1" },
  ];

  it("renders the saved profile, its trust trade-off and the daemon's problems", async () => {
    fakeDaemon({
      [`GET ${P}/executors/config`]: config(
        { kind: "hermes", model: "openrouter/qwen" },
        {
          compat: { kind: "hermes", model: "openrouter/qwen", blocked: true, message: "hermes cannot call tools with this model." },
          problems: [{ severity: "error", field: "image", message: "image is not pulled" }],
        },
      ),
      [`GET ${P}/executors`]: rows,
    });
    render(<ExecutorSettings projectId="p1" />);
    await waitFor(() => expect((screen.getByLabelText("Kind") as HTMLSelectElement).value).toBe("hermes"));
    expect((screen.getByLabelText("Model") as HTMLInputElement).value).toBe("openrouter/qwen");
    expect(screen.getByTestId("executor-trust").textContent).toMatch(/--yolo auto-approve/);
    expect(screen.getByText(/Known not to work: hermes cannot call tools/)).toBeTruthy();
    expect(screen.getByText("image is not pulled")).toBeTruthy();
    // Nothing changed yet: the smoke test runs the saved profile.
    expect(screen.queryByRole("button", { name: "Discard changes" })).toBeNull();
  });

  it("refuses acp without an image or command, and a secret-looking MCP literal, without a PUT", async () => {
    const { requests } = fakeDaemon({
      [`GET ${P}/executors/config`]: config({ kind: "acp", image: "", mcp_servers: [] }),
      [`GET ${P}/executors`]: rows,
    });
    render(<ExecutorSettings projectId="p1" />);
    await waitFor(() => expect((screen.getByLabelText("Kind") as HTMLSelectElement).value).toBe("acp"));
    fireEvent.click(screen.getByRole("button", { name: "Add MCP server" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "github" } });
    fireEvent.change(screen.getByLabelText("Command"), { target: { value: "github-mcp" } });
    fireEvent.click(screen.getByRole("button", { name: "Add variable" }));
    fireEvent.change(screen.getByLabelText("Variable name", { selector: "input[aria-label='Variable name']:not([placeholder='VARIABLE_NAME'])" }), {
      target: { value: "GH_HOST" },
    });
    fireEvent.change(screen.getByLabelText("Value source"), { target: { value: "value" } });
    fireEvent.change(screen.getByLabelText("Literal value"), { target: { value: "ghp_abcdefghijklmnop" } });
    fireEvent.click(screen.getByRole("button", { name: "Save executor" }));

    expect(await screen.findByText(/acp needs an image/)).toBeTruthy();
    expect(screen.getByText(/acp needs the agent's argv/)).toBeTruthy();
    expect(screen.getByText(/This looks like a credential/)).toBeTruthy();
    expect(screen.getByLabelText("Image").getAttribute("aria-invalid")).toBe("true");
    expect(requests.filter((r) => r.method === "PUT")).toHaveLength(0);
  });

  it("sends only what changed, reports saved and the daemon's warnings, and says when nothing changed", async () => {
    const { requests } = fakeDaemon({
      [`GET ${P}/executors/config`]: config({ kind: "hermes", model: "" }),
      [`GET ${P}/executors`]: rows,
      [`PUT ${P}/executors`]: { status: "updated", warnings: ["The image is not pulled yet; the first run pulls it."] },
    });
    render(<ExecutorSettings projectId="p1" />);
    await waitFor(() => expect((screen.getByLabelText("Kind") as HTMLSelectElement).value).toBe("hermes"));
    fireEvent.click(screen.getByRole("button", { name: "Save executor" }));
    expect(await screen.findByText("Executor is unchanged.")).toBeTruthy();
    expect(requests.filter((r) => r.method === "PUT")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Image"), { target: { value: "ghcr.io/me/hermes:2" } });
    // Edited and unsaved: the smoke test would not run what is on screen.
    expect(screen.getByText(/Save the executor first/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Save executor" }));
    expect(await screen.findByText("Executor saved.")).toBeTruthy();
    expect(screen.getByText("The image is not pulled yet; the first run pulls it.")).toBeTruthy();
    expect(requests.find((r) => r.method === "PUT")?.body).toEqual({ image: "ghcr.io/me/hermes:2" });
  });

  it("puts a model refusal next to Model, and any other refusal on the page", async () => {
    const daemon = (error: string) =>
      fakeDaemon({
        [`GET ${P}/executors/config`]: config({ kind: "hermes", model: "" }),
        [`GET ${P}/executors`]: rows,
        [`PUT ${P}/executors`]: fail(422, error),
      });
    daemon("unknown model provider \"nope\"");
    const { unmount } = render(<ExecutorSettings projectId="p1" />);
    await waitFor(() => expect((screen.getByLabelText("Kind") as HTMLSelectElement).value).toBe("hermes"));
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "nope/x" } });
    fireEvent.click(screen.getByRole("button", { name: "Save executor" }));
    const msg = await screen.findByText('unknown model provider "nope"');
    // Directly under the Model field.
    expect(msg.previousElementSibling).toBe(screen.getByLabelText("Model"));
    unmount();

    daemon("executor.image: invalid reference format");
    render(<ExecutorSettings projectId="p1" />);
    await waitFor(() => expect((screen.getByLabelText("Kind") as HTMLSelectElement).value).toBe("hermes"));
    fireEvent.change(screen.getByLabelText("Image"), { target: { value: "UPPER" } });
    fireEvent.click(screen.getByRole("button", { name: "Save executor" }));
    const general = await screen.findByText("executor.image: invalid reference format");
    expect(general.previousElementSibling?.getAttribute("id")).not.toBe("executor-model");
  });

  it("refuses plain http to another machine for OpenHands", async () => {
    const { requests } = fakeDaemon({
      [`GET ${P}/executors/config`]: config({ kind: "openhands", openhands: { url: "" } }),
      [`GET ${P}/executors`]: rows,
    });
    render(<ExecutorSettings projectId="p1" />);
    await waitFor(() => expect((screen.getByLabelText("Kind") as HTMLSelectElement).value).toBe("openhands"));
    expect(screen.getByText(/OPENHANDS_API_KEY/, { selector: "code" }).parentElement!.textContent).toMatch(/is not set/);
    fireEvent.change(screen.getByLabelText("Server URL"), { target: { value: "http://agents.example.com" } });
    fireEvent.change(screen.getByLabelText("Session key variable"), { target: { value: "sk-live-key" } });
    fireEvent.click(screen.getByRole("button", { name: "Save executor" }));
    expect(await screen.findByText(/in the clear/)).toBeTruthy();
    expect(screen.getByText(/never the key itself/)).toBeTruthy();
    expect(requests.filter((r) => r.method === "PUT")).toHaveLength(0);
  });
});

describe("BudgetSettings", () => {
  const meter = {
    cap_usd: 5,
    source: "project",
    initiative_cap_usd: null,
    project_default_usd: 5,
    spent_usd: 5.2,
    model_usd: 5.2,
    agent_usd: null,
    agent_runs: 0,
    agent_not_counted: false,
    remaining_usd: 0,
    exceeded: true,
    parked: true,
    note: "",
  };
  const view = { default_initiative_usd: 5, initiatives: [{ initiative_id: "i1", title: "Recipe app", status: "needs_human_judgment", budget: meter }] };

  it("renders the default and every initiative's meter, each linking to its Build page", async () => {
    fakeDaemon({ [`GET ${P}/budget`]: view });
    render(<BudgetSettings projectId="p1" />);
    await waitFor(() => expect((screen.getByLabelText(/Default cap per initiative/) as HTMLInputElement).value).toBe("5"));
    expect(screen.getByRole("link", { name: "Recipe app" }).getAttribute("href")).toBe("#/i/i1/build");
    expect(screen.getByText(/\$5\.20 of \$5\.00 \(project default\) — stopped at its cap/)).toBeTruthy();
  });

  it("refuses a negative or non-numeric default without sending it", async () => {
    const { requests } = fakeDaemon({ [`GET ${P}/budget`]: view });
    render(<BudgetSettings projectId="p1" />);
    const input = await screen.findByLabelText(/Default cap per initiative/);
    fireEvent.change(input, { target: { value: "-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Save default" }));
    expect(await screen.findByText(/A dollar amount/)).toBeTruthy();
    expect(requests.filter((r) => r.method === "PUT")).toHaveLength(0);
  });

  it("saves a new default and says how many stopped initiatives went back", async () => {
    const { requests } = fakeDaemon({ [`GET ${P}/budget`]: view, [`PUT ${P}/budget`]: { resumed: ["i1"] } });
    render(<BudgetSettings projectId="p1" />);
    const input = await screen.findByLabelText(/Default cap per initiative/);
    fireEvent.change(input, { target: { value: "$10" } });
    fireEvent.click(screen.getByRole("button", { name: "Save default" }));
    expect(await screen.findByText(/capped at \$10\.00\. 1 stopped initiative\(s\) went back/)).toBeTruthy();
    expect(requests.find((r) => r.method === "PUT")?.body).toEqual({ default_initiative_usd: 10 });
  });

  it("Remove default sends 0, and a refused write is shown", async () => {
    const { requests } = fakeDaemon({ [`GET ${P}/budget`]: view, [`PUT ${P}/budget`]: fail(422, "budget: harness.yaml is not writable") });
    render(<BudgetSettings projectId="p1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Remove default" }));
    expect(await screen.findByText("budget: harness.yaml is not writable")).toBeTruthy();
    expect(requests.find((r) => r.method === "PUT")?.body).toEqual({ default_initiative_usd: 0 });
  });
});

describe("AutomationsPage", () => {
  const evalJob = {
    name: "eval",
    label: "Scheduled eval",
    description: "Runs the eval suite.",
    enabled: false,
    available: true,
    schedule: "every 24h",
    running: false,
    last_run: null,
    next_run_at: null,
    settings: { enabled: false, model: "openai/gpt-4o-mini", max_cost_usd: 2 },
    history: [
      { id: "r1", automation: "eval", trigger: "manual", status: "failed", started_at_ms: Date.now() - 3_600_000, cost_usd: 0.42, error: "suite not found" },
    ],
  };
  const linearJob = {
    name: "linear_inbound",
    label: "Linear inbound",
    description: "Imports labeled issues.",
    enabled: false,
    available: false,
    unavailable: "Linear is not configured.",
    schedule: "every 60s",
    running: false,
    last_run: null,
    next_run_at: null,
    history: [],
  };
  const A = `${P}/automations`;

  it("renders each job off, its schedule, why it is unavailable, and its history", async () => {
    fakeDaemon({ [`GET ${A}`]: { automations: [evalJob, linearJob] } });
    render(<AutomationsPage projectId="p1" />);
    const ev = (await screen.findByRole("heading", { name: "Scheduled eval" })).closest("section")!;
    expect(within(ev).getByRole("switch").getAttribute("aria-checked")).toBe("false");
    expect(within(ev).getByTestId("next-eval").textContent).toMatch(/Off — nothing runs/);
    expect(within(ev).getByText("suite not found")).toBeTruthy();
    expect(within(ev).getByText("failed")).toBeTruthy();
    const lin = screen.getByRole("heading", { name: "Linear inbound" }).closest("section")!;
    expect(within(lin).getByText("Linear is not configured.")).toBeTruthy();
    // Cannot be turned on, or run, until Linear is set up.
    expect((within(lin).getByRole("switch") as HTMLInputElement).disabled).toBe(true);
    expect((within(lin).getByRole("button", { name: "Run now" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("turns a job on with a PUT, and puts the switch back when the write is refused", async () => {
    const { requests } = fakeDaemon({
      [`GET ${A}`]: { automations: [evalJob] },
      [`PUT ${A}/eval`]: fail(409, "eval: no provider key for openai"),
    });
    render(<AutomationsPage projectId="p1" />);
    const sw = (await screen.findByRole("switch")) as HTMLInputElement;
    fireEvent.click(sw);
    expect(await screen.findByText("eval: no provider key for openai")).toBeTruthy();
    expect(requests.find((r) => r.method === "PUT")?.body).toEqual({ enabled: true });
    await waitFor(() => expect(sw.checked).toBe(false));
  });

  it("validates the eval settings (cost cap > 0, relative paths) before saving them", async () => {
    const { requests } = fakeDaemon({ [`GET ${A}`]: { automations: [evalJob] }, [`PUT ${A}/eval`]: { status: "updated" } });
    render(<AutomationsPage projectId="p1" />);
    const cap = await screen.findByLabelText("Cost cap per run (USD)");
    fireEvent.change(cap, { target: { value: "0" } });
    fireEvent.change(screen.getByLabelText(/Baseline report/), { target: { value: "../outside.json" } });
    fireEvent.click(screen.getByRole("button", { name: "Save eval settings" }));
    expect(await screen.findByText(/the most one run may spend, in dollars \(> 0\)/)).toBeTruthy();
    expect(screen.getByText(/A path relative to the project root/)).toBeTruthy();
    expect(cap.getAttribute("aria-invalid")).toBe("true");
    expect(requests.filter((r) => r.method === "PUT")).toHaveLength(0);

    fireEvent.change(cap, { target: { value: "1.5" } });
    fireEvent.change(screen.getByLabelText(/Baseline report/), { target: { value: "evals/baseline.json" } });
    fireEvent.click(screen.getByRole("button", { name: "Save eval settings" }));
    expect(await screen.findByText("Eval settings saved.")).toBeTruthy();
    expect(requests.find((r) => r.method === "PUT")?.body).toMatchObject({ model: "openai/gpt-4o-mini", max_cost_usd: 1.5, baseline: "evals/baseline.json" });
  });

  it("Run now posts once and says it started", async () => {
    const { requests } = fakeDaemon({ [`GET ${A}`]: { automations: [evalJob] }, [`POST ${A}/eval/run`]: { runs: 1 } });
    render(<AutomationsPage projectId="p1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Run now" }));
    expect(await screen.findByText("Started.")).toBeTruthy();
    expect(requests.filter((r) => r.method === "POST")).toEqual([{ method: "POST", path: `${A}/eval/run`, body: {} }]);
  });

  it("shows why the list cannot be read", async () => {
    fakeDaemon({ [`GET ${A}`]: fail(500, "automations: store unavailable") });
    render(<AutomationsPage projectId="p1" />);
    expect(await screen.findByText("automations: store unavailable")).toBeTruthy();
  });
});
