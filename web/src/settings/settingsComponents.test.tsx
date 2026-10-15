// @vitest-environment jsdom
//
// Component tests for the Phase 4 settings editors: what they refuse before
// a round trip, and what they send when they do send.
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import IntegrationEditor from "./IntegrationEditor";
import BudgetMeter from "../BudgetMeter";
import { fakeDaemon } from "../testing/fakeDaemon";

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
});
