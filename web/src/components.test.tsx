// @vitest-environment jsdom
//
// Component tests for the UI review fixes: they render real components in
// jsdom against a stubbed daemon (testing/fakeDaemon.ts), where the pure
// *.test.ts files beside them can only check the helpers. The rest of the
// suite stays on vitest's default node environment; this file opts in.
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import NoteField from "./NoteField";
import NotFound from "./NotFound";
import FailedBanner from "./FailedBanner";
import Settings from "./Settings";
import DecisionPage from "./DecisionPage";
import BuildPage from "./BuildPage";
import { changeSetChip } from "./statusChips";
import { fakeDaemon } from "./testing/fakeDaemon";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

const P = "/api/v1/projects/p1";

describe("NoteField", () => {
  it("is a labelled textarea, so the field can be found by its purpose", () => {
    const onChange = vi.fn();
    render(<NoteField id="n" label="What should change in CHG-1?" value="" onChange={onChange} />);
    const box = screen.getByLabelText("What should change in CHG-1?");
    expect(box.tagName).toBe("TEXTAREA");
    fireEvent.change(box, { target: { value: "use the logger" } });
    expect(onChange).toHaveBeenCalledWith("use the logger");
  });
});

describe("NotFound", () => {
  it("says what was not found and links back", () => {
    render(
      <NotFound
        title="Decision not found"
        detail={`There's no decision "x".`}
        back={{ label: "← Back to Define", href: "#/i/abc/define" }}
      />,
    );
    expect(screen.getByRole("heading", { name: "Decision not found" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "← Back to Define" }));
    expect(window.location.hash).toBe("#/i/abc/define");
  });
});

describe("changeSetChip", () => {
  it("reads applied, with demo as a marker, for placeholder output", () => {
    expect(changeSetChip({ status: "applied", executor: "internal" })).toEqual({
      label: "applied",
      state: "verified",
      marker: "demo",
    });
  });
});

describe("FailedBanner", () => {
  it("sends the retry note with the initiative retry", async () => {
    const { requests } = fakeDaemon({
      [`POST ${P}/initiatives/i1/retry`]: { operation_id: "op1" },
      [`GET ${P}/initiatives/i1/operations`]: [{ id: "op1", status: "running" }],
    });
    render(<FailedBanner projectId="p1" initiativeId="i1" reason="model timed out" defaultModel="openai/gpt-5" />);
    fireEvent.change(screen.getByLabelText("Note for the retry (optional)"), {
      target: { value: "keep the PRD short" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(requests.some((r) => r.method === "POST")).toBe(true));
    const post = requests.find((r) => r.method === "POST")!;
    expect(post.body).toEqual({ model: "openai/gpt-5", note: "keep the PRD short" });
  });

  it("passes the note to a caller's own retry", async () => {
    fakeDaemon({ [`GET ${P}/initiatives/i1/operations`]: [] });
    const onRetry = vi.fn(async () => "op2");
    render(<FailedBanner projectId="p1" initiativeId="i1" title="Build failed" onRetry={onRetry} />);
    fireEvent.change(screen.getByLabelText("Note for the retry (optional)"), { target: { value: "smaller diff" } });
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(onRetry).toHaveBeenCalledWith("smaller diff"));
  });
});

describe("DecisionPage", () => {
  it("renders not-found, and no Send box, for a decision that does not exist", async () => {
    fakeDaemon({
      [`GET ${P}/initiatives/i1/decisions`]: [
        { id: "d1", code: "DEC-1", question: "Q?", recommendation: "", options: [], status: "open" },
      ],
      [`GET ${P}/decisions/nope/discussion`]: [],
      [`GET ${P}/initiatives/i1/changed`]: { groups: [], through: 0 },
    });
    render(<DecisionPage projectId="p1" initiativeId="i1" decisionId="nope" />);
    expect(await screen.findByRole("heading", { name: "Decision not found" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Send" })).toBeNull();
  });
});

describe("Settings executor", () => {
  const providers = {
    default_model: "",
    demo: true,
    providers: [
      { name: "openai", label: "OpenAI", type: "openai", base_url: "", key_env: "OPENAI_API_KEY", ready: false, key_set: false, configured: false, is_default: false, models: [] },
      { name: "ollama", label: "Ollama (local)", type: "openai", base_url: "", key_env: "", ready: true, key_set: true, configured: false, is_default: false, models: ["qwen"] },
    ],
  };
  const internalRow = {
    name: "internal",
    trust: "internal",
    sandbox_mode: "local_restricted",
    network: false,
    capabilities: ["fs.write"],
    selected: false,
    image: "",
    default_image: "",
    status: { image: "", image_present: true, key_env: "", key_set: true, problem: "" },
    resolution: { requested: "auto", resolved: "internal", warning: "NO REAL CODE WILL BE WRITTEN: …" },
  };

  it("shows the requested kind (auto) and what it resolves to, not a default of its own", async () => {
    fakeDaemon({
      [`GET ${P}/providers`]: providers,
      [`GET ${P}/executors`]: [internalRow, { ...internalRow, name: "hermes", resolution: undefined, trust: "local_external" }],
      [`GET ${P}/secret-allowlist`]: [],
      [`GET ${P}/models`]: { provider: "ollama", models: ["qwen"] },
      [`GET ${P}/integrations`]: { integrations: [] },
    });
    render(<Settings projectId="p1" />);
    await waitFor(() => expect((screen.getByLabelText("Kind") as HTMLSelectElement).value).toBe("auto"));
    expect(await screen.findByText("Requested: auto · resolves to: internal.")).toBeTruthy();
    // Nothing about an image the internal executor does not have.
    expect(screen.queryByText(/default image is pulled/)).toBeNull();
    // A ready provider in demo mode says it is not the one selected.
    expect(screen.getByText("ready · not selected")).toBeTruthy();
    // No banner of its own (the shell has one), and no unprompted key error.
    expect(screen.queryByText(/Demo mode/)).toBeNull();
    expect(screen.queryByText(/OPENAI_API_KEY is not set/)).toBeNull();
  });
});

describe("BuildPage review actions", () => {
  const cs = {
    id: "cs1",
    human_id: "CHG-1",
    task_id: "t1",
    status: "review_required",
    patch_digest: "abc",
    executor: "internal",
    review_completed_at: "2026-10-14T00:00:00Z",
  };
  const passingCheck = {
    id: "f1",
    severity: "observation",
    category: "patch_applies",
    finding: "patch applies — pass",
    recommendation: "",
    target_type: "change_set",
    target_id: "cs1",
  };

  it("sends Request changes with the note, and offers no 'accepting findings' with nothing to accept", async () => {
    const { requests } = fakeDaemon({
      [`GET ${P}/initiatives/i1/tasks`]: [{ id: "t1", code: "TASK-1", title: "Implement", status: "proposed" }],
      [`GET ${P}/initiatives/i1/change-sets`]: [cs],
      [`GET ${P}/initiatives/i1/findings`]: [passingCheck],
      [`POST ${P}/change-sets/cs1/review`]: { status: "reviewed" },
    });
    render(<BuildPage projectId="p1" initiativeId="i1" />);
    await screen.findByText("CHG-1");
    expect(screen.queryByRole("button", { name: /Approve, accepting/ })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Request changes…" }));
    fireEvent.change(screen.getByLabelText("What should change in CHG-1?"), { target: { value: "rename the file" } });
    fireEvent.click(screen.getByRole("button", { name: "Send back with this note" }));
    await waitFor(() => expect(requests.some((r) => r.method === "POST")).toBe(true));
    expect(requests.find((r) => r.method === "POST")!.body).toEqual({
      verdict: "changes_requested",
      note: "rename the file",
    });
  });

  it("offers 'accepting N findings' only for open blocking reviewer findings", async () => {
    fakeDaemon({
      [`GET ${P}/initiatives/i1/change-sets`]: [cs],
      [`GET ${P}/initiatives/i1/findings`]: [
        passingCheck,
        { ...passingCheck, id: "f2", severity: "high", category: "quality", finding: "implements nothing" },
      ],
    });
    render(<BuildPage projectId="p1" initiativeId="i1" />);
    expect(await screen.findByRole("button", { name: "Approve, accepting 1 finding" })).toBeTruthy();
  });

  it("labels an applied placeholder change set applied, with a demo marker", async () => {
    fakeDaemon({ [`GET ${P}/initiatives/i1/change-sets`]: [{ ...cs, status: "applied" }] });
    render(<BuildPage projectId="p1" initiativeId="i1" />);
    expect(await screen.findByText("applied")).toBeTruthy();
    expect(screen.getByText("demo")).toBeTruthy();
    expect(screen.queryByText(/Placeholder change/)).toBeNull();
  });
});
