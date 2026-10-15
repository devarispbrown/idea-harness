// @vitest-environment jsdom
//
// Keyboard and accessibility behaviour, rendered (docs/ui.md "Keyboard and
// accessibility"): ⌘/Ctrl+Enter in note fields, Escape and focus return in
// disclosures, the `?` reference and the ⌘K palette as modal dialogs, the
// live region, and Define's objective/metric edits and unlink.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import NoteField from "./NoteField";
import InlinePanel from "./InlinePanel";
import ShortcutsDialog from "./ShortcutsDialog";
import CommandPalette from "./CommandPalette";
import LiveAnnouncer from "./LiveAnnouncer";
import ObjectivesSection from "./ObjectivesSection";
import { announce, HOLD_MS, resetAnnouncements, RUN_RANK, runTopic, SETTLE_MS } from "./announce";
import { fakeDaemon } from "./testing/fakeDaemon";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

// useDialog moves focus on the next animation frame.
const nextFrame = () => act(() => new Promise<void>((r) => requestAnimationFrame(() => r())));

describe("NoteField shortcuts", () => {
  it("sends on ⌘+Enter and Ctrl+Enter, keeps plain Enter a newline, and says so under the field", () => {
    const onSubmit = vi.fn();
    render(<NoteField id="n" label="Note" value="x" onChange={() => {}} onSubmit={onSubmit} submitLabel="send it" />);
    const box = screen.getByLabelText("Note");
    fireEvent.keyDown(box, { key: "Enter" });
    expect(onSubmit).not.toHaveBeenCalled();
    fireEvent.keyDown(box, { key: "Enter", metaKey: true });
    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true });
    expect(onSubmit).toHaveBeenCalledTimes(2);
    // The hint is the field's description, so a screen reader reads it too.
    const hint = document.getElementById(box.getAttribute("aria-describedby")!)!;
    expect(hint.textContent).toMatch(/Enter to send it/);
  });

  it("has no shortcut, and no hint, without onSubmit", () => {
    render(<NoteField id="n" label="Note" value="" onChange={() => {}} />);
    expect(screen.getByLabelText("Note").getAttribute("aria-describedby")).toBeNull();
  });
});

function Disclosure() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button onClick={() => setOpen(true)}>Request changes…</button>
      {open && (
        <InlinePanel label="Request changes" onClose={() => setOpen(false)}>
          <NoteField id="rc" label="What should change?" value="" onChange={() => {}} />
          <button onClick={() => setOpen(false)}>Cancel</button>
        </InlinePanel>
      )}
    </>
  );
}

describe("InlinePanel", () => {
  it("moves focus in on open, closes on Escape, and returns focus to the button that opened it", async () => {
    render(<Disclosure />);
    const opener = screen.getByRole("button", { name: "Request changes…" });
    opener.focus();
    fireEvent.click(opener);
    await nextFrame();
    const field = screen.getByLabelText("What should change?");
    expect(document.activeElement).toBe(field);
    fireEvent.keyDown(field, { key: "Escape" });
    expect(screen.queryByLabelText("What should change?")).toBeNull();
    expect(document.activeElement).toBe(opener);
  });
});

describe("ShortcutsDialog", () => {
  it("opens on ?, traps Tab, closes on Escape and gives focus back", async () => {
    render(
      <>
        <button>before</button>
        <input aria-label="search" />
        <ShortcutsDialog />
      </>,
    );
    const before = screen.getByRole("button", { name: "before" });
    before.focus();
    fireEvent.keyDown(window, { key: "?", shiftKey: true });
    const dialog = await screen.findByRole("dialog", { name: "Keyboard shortcuts" });
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    await nextFrame();
    const close = screen.getByRole("button", { name: "Close" });
    expect(document.activeElement).toBe(close);
    // Tab from the only control wraps to it, never out of the dialog.
    fireEvent.keyDown(document, { key: "Tab" });
    expect(dialog.contains(document.activeElement)).toBe(true);
    expect(screen.getByText(/Open the command palette/)).toBeTruthy();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.activeElement).toBe(before);
  });

  it("does not open while typing a question mark into a field", () => {
    render(
      <>
        <input aria-label="search" />
        <ShortcutsDialog />
      </>,
    );
    const input = screen.getByLabelText("search");
    input.focus();
    fireEvent.keyDown(input, { key: "?" });
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});

describe("CommandPalette", () => {
  const open = async () => {
    fireEvent.keyDown(window, { key: "k", metaKey: true });
    await screen.findByRole("dialog", { name: "Command palette" });
    await nextFrame();
    return screen.getByRole("combobox");
  };

  it("offers the Product Graph views, Activity, Needs you and Settings sections", async () => {
    render(<CommandPalette initiativeId="i1" onNavigate={() => {}} />);
    await open();
    const labels = screen.getAllByRole("option").map((o) => o.querySelector(".cmd-label")!.textContent);
    for (const want of ["Gaps", "Outcomes", "Quality", "Activity", "Needs you", "Settings", "Settings › Executor"]) {
      expect(labels).toContain(want);
    }
  });

  it("opens a typed node code's lineage", async () => {
    const onNavigate = vi.fn();
    render(<CommandPalette initiativeId="i1" onNavigate={onNavigate} />);
    const input = await open();
    fireEvent.change(input, { target: { value: "req-3" } });
    expect(screen.getAllByRole("option")[0].textContent).toContain("Lineage of REQ-3");
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onNavigate).toHaveBeenCalledWith("#/i/i1/lineage/requirement/REQ-3");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("matches keywords: 'timeline' finds Activity", async () => {
    render(<CommandPalette initiativeId="i1" onNavigate={() => {}} />);
    const input = await open();
    fireEvent.change(input, { target: { value: "timeline" } });
    expect(screen.getAllByRole("option").map((o) => o.querySelector(".cmd-label")!.textContent)).toEqual(["Activity"]);
  });
});

describe("LiveAnnouncer", () => {
  it("is one polite status region that speaks what announce() says, the same message twice too", () => {
    render(<LiveAnnouncer />);
    const region = screen.getByTestId("live-announcer");
    expect(region.getAttribute("aria-live")).toBe("polite");
    act(() => announce("CHG-1 approved."));
    expect(region.textContent).toBe("CHG-1 approved.");
    act(() => announce("CHG-1 approved."));
    // Changed text, so a screen reader announces the repeat.
    expect(region.textContent).not.toBe("CHG-1 approved.");
    expect(region.textContent!.trim()).toBe("CHG-1 approved.");
  });
});

describe("LiveAnnouncer: one message per run completion", () => {
  // Every message the region showed, in order.
  const heard = () => {
    const said: string[] = [];
    const region = screen.getByTestId("live-announcer");
    const obs = new MutationObserver(() => {
      if (region.textContent) said.push(region.textContent.trim());
    });
    obs.observe(region, { childList: true, characterData: true, subtree: true });
    return { said, stop: () => obs.disconnect() };
  };
  const run = { topic: runTopic("i1") };

  afterEach(() => {
    vi.useRealTimers();
    resetAnnouncements();
  });

  it("a run that starts and ends within one poll is said once, as the operation's end", async () => {
    vi.useFakeTimers();
    render(<LiveAnnouncer />);
    const h = heard();
    act(() => announce("Agent run started.", { ...run, rank: RUN_RANK.runStarted }));
    act(() => announce("Executing the plan finished.", { ...run, rank: RUN_RANK.operationEnded }));
    await act(async () => vi.advanceTimersByTime(HOLD_MS));
    await Promise.resolve();
    h.stop();
    expect(h.said).toEqual(["Executing the plan finished."]);
  });

  it("drops a less specific echo that arrives just after the end, but not a later run", async () => {
    vi.useFakeTimers();
    render(<LiveAnnouncer />);
    const region = screen.getByTestId("live-announcer");
    act(() => announce("Executing the plan stopped.", { ...run, rank: RUN_RANK.operationEnded }));
    await act(async () => vi.advanceTimersByTime(HOLD_MS));
    expect(region.textContent).toBe("Executing the plan stopped.");
    // The stopped card's panel catching up on the same run.
    act(() => announce("Agent run started.", { ...run, rank: RUN_RANK.runStarted }));
    await act(async () => vi.advanceTimersByTime(HOLD_MS));
    expect(region.textContent).toBe("Executing the plan stopped.");
    // A run started well after that is news again.
    await act(async () => vi.advanceTimersByTime(SETTLE_MS));
    act(() => announce("Agent run started.", { ...run, rank: RUN_RANK.runStarted }));
    await act(async () => vi.advanceTimersByTime(HOLD_MS));
    expect(region.textContent).toBe("Agent run started.");
  });

  it("keeps other initiatives' runs and untopiced messages apart", async () => {
    vi.useFakeTimers();
    render(<LiveAnnouncer />);
    const region = screen.getByTestId("live-announcer");
    act(() => announce("Executing the plan finished.", { ...run, rank: RUN_RANK.operationEnded }));
    await act(async () => vi.advanceTimersByTime(HOLD_MS / 2));
    act(() => announce("Agent run started.", { topic: runTopic("i2"), rank: RUN_RANK.runStarted }));
    act(() => announce("CHG-1 approved."));
    // Untopiced: said at once.
    expect(region.textContent).toBe("CHG-1 approved.");
    await act(async () => vi.advanceTimersByTime(HOLD_MS / 2));
    expect(region.textContent).toBe("Executing the plan finished.");
    // Another initiative's run is not this one's echo.
    await act(async () => vi.advanceTimersByTime(HOLD_MS / 2));
    expect(region.textContent).toBe("Agent run started.");
  });
});

describe("ObjectivesSection edits", () => {
  const P = "/api/v1/projects/p1/initiatives/i1";
  const objective = {
    id: "o1",
    code: "OBJ-1",
    statement: "Cooks share recipes",
    rationale: "",
    status: "active",
    requirement_ids: ["r1"],
    metric_ids: ["m1"],
  };
  const metric = {
    id: "m1",
    code: "MET-1",
    objective_id: "o1",
    objective_code: "OBJ-1",
    name: "Shares per week",
    direction: "increase",
    baseline: 10,
    target: 50,
    source: { kind: "manual" },
    status: { drift: "no_data" },
  };
  const daemon = () =>
    fakeDaemon({
      [`GET ${P}/objectives`]: [objective],
      [`GET ${P}/metrics`]: [metric],
      [`GET ${P}/requirements`]: [{ id: "r1", code: "REQ-1", statement: "Share a recipe", objective_ids: ["o1"] }],
      [`PATCH ${P}/objectives/OBJ-1`]: objective,
      [`POST ${P}/objectives/OBJ-1/requirements`]: objective,
      [`PATCH ${P}/metrics/MET-1`]: metric,
    });

  it("edits an objective, sending only what changed", async () => {
    const { requests } = daemon();
    render(<ObjectivesSection projectId="p1" initiativeId="i1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit OBJ-1" }));
    fireEvent.change(screen.getByLabelText("Statement"), { target: { value: "Cooks share family recipes" } });
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "achieved" } });
    fireEvent.click(screen.getByRole("button", { name: "Save OBJ-1" }));
    await waitFor(() => expect(requests.some((r) => r.method === "PATCH")).toBe(true));
    expect(requests.find((r) => r.method === "PATCH")).toEqual({
      method: "PATCH",
      path: `${P}/objectives/OBJ-1`,
      body: { statement: "Cooks share family recipes", status: "achieved" },
    });
  });

  it("unlinks a requirement", async () => {
    const { requests } = daemon();
    render(<ObjectivesSection projectId="p1" initiativeId="i1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Unlink REQ-1 from OBJ-1" }));
    await waitFor(() => expect(requests.some((r) => r.method === "POST")).toBe(true));
    expect(requests.find((r) => r.method === "POST")!.body).toEqual({ requirement: "REQ-1", unlink: true });
  });

  it("edits a metric: a cleared baseline is clear_baseline, not an omitted field", async () => {
    const { requests } = daemon();
    render(<ObjectivesSection projectId="p1" initiativeId="i1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit MET-1" }));
    fireEvent.change(screen.getByLabelText("MET-1 baseline (blank for none)"), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText("MET-1 target"), { target: { value: "60" } });
    fireEvent.click(screen.getByRole("button", { name: "Save MET-1" }));
    await waitFor(() => expect(requests.some((r) => r.method === "PATCH")).toBe(true));
    expect(requests.find((r) => r.method === "PATCH")!.body).toEqual({ target: 60, clear_baseline: true });
  });
});
