import { describe, expect, it } from "vitest";
import { isEditableTarget, isHelpShortcut, isMac, isSubmitShortcut, modLabel, shortcutGroups } from "./shortcuts";
import { filterCommands, nodeCodeCommand, paletteCommands, SETTINGS_SECTIONS } from "./paletteCommands";
import { nextAnnouncement, operationAnnouncement, runStatusAnnouncement } from "./announce";

describe("shortcut predicates", () => {
  it("⌘/Ctrl+Enter submits; plain Enter, Alt and IME composition do not", () => {
    expect(isSubmitShortcut({ key: "Enter", metaKey: true })).toBe(true);
    expect(isSubmitShortcut({ key: "Enter", ctrlKey: true })).toBe(true);
    expect(isSubmitShortcut({ key: "Enter" })).toBe(false);
    expect(isSubmitShortcut({ key: "Enter", ctrlKey: true, altKey: true })).toBe(false);
    expect(isSubmitShortcut({ key: "Enter", metaKey: true, isComposing: true })).toBe(false);
  });

  it("? opens help only outside text fields", () => {
    const field = (tagName: string, type?: string) => ({ tagName, type, isContentEditable: false }) as unknown as EventTarget;
    expect(isHelpShortcut({ key: "?", shiftKey: true, target: field("BUTTON") })).toBe(true);
    expect(isHelpShortcut({ key: "?", target: field("TEXTAREA") })).toBe(false);
    expect(isHelpShortcut({ key: "?", target: field("INPUT", "text") })).toBe(false);
    expect(isHelpShortcut({ key: "?", target: field("INPUT", "checkbox") })).toBe(true);
    expect(isHelpShortcut({ key: "?", ctrlKey: true, target: field("DIV") })).toBe(false);
    expect(isEditableTarget(null)).toBe(false);
  });

  it("names ⌘ on a Mac and Ctrl elsewhere", () => {
    expect(isMac("MacIntel")).toBe(true);
    expect(isMac("Win32")).toBe(false);
    expect(modLabel(true)).toBe("⌘");
    expect(modLabel(false)).toBe("Ctrl");
    const keys = shortcutGroups(false).flatMap((g) => g.items.map((i) => i.keys.join("+")));
    expect(keys).toEqual(expect.arrayContaining(["Ctrl+K", "?", "Ctrl+Enter", "Esc"]));
  });
});

describe("palette commands", () => {
  it("lists the views of an open initiative, and only the project-wide ones without one", () => {
    const withIni = paletteCommands("i1").map((c) => c.href);
    expect(withIni).toEqual(
      expect.arrayContaining(["#/i/i1/gaps", "#/i/i1/outcomes", "#/i/i1/quality", "#/i/i1/changed", "#/i/i1/health", "#/queue", "#/settings"]),
    );
    const bare = paletteCommands("").map((c) => c.href);
    expect(bare.some((h) => h.startsWith("#/i/"))).toBe(false);
    expect(bare).toContain("#/queue");
  });

  it("has one entry per Settings section, each naming its heading", () => {
    const sections = paletteCommands("").filter((c) => c.section);
    expect(sections.map((c) => c.section)).toEqual(SETTINGS_SECTIONS);
    expect(sections.every((c) => c.href === "#/settings")).toBe(true);
  });

  it("turns a node code into its lineage, case-insensitively, and nothing else", () => {
    expect(nodeCodeCommand("i1", "task-2")?.href).toBe("#/i/i1/lineage/task/TASK-2");
    expect(nodeCodeCommand("i1", " OBJ-1 ")?.href).toBe("#/i/i1/lineage/objective/OBJ-1");
    expect(nodeCodeCommand("i1", "MET-01")?.label).toBe("Lineage of MET-1");
    expect(nodeCodeCommand("i1", "CHG-4")?.href).toBe("#/i/i1/lineage/change_set/CHG-4");
    expect(nodeCodeCommand("i1", "DEC-2")?.href).toBe("#/i/i1/lineage/decision/DEC-2");
    expect(nodeCodeCommand("i1", "FOO-1")).toBeNull();
    expect(nodeCodeCommand("i1", "REQ")).toBeNull();
    // Codes are per initiative: none open, nothing to resolve against.
    expect(nodeCodeCommand("", "REQ-1")).toBeNull();
  });

  it("filters by label or keyword, with a code's lineage first", () => {
    const all = paletteCommands("i1");
    expect(filterCommands(all, "i1", "")).toHaveLength(all.length);
    expect(filterCommands(all, "i1", "drift").map((c) => c.id)).toEqual(["outcomes"]);
    expect(filterCommands(all, "i1", "REQ-2")[0].label).toBe("Lineage of REQ-2");
    expect(filterCommands(all, "i1", "executor").map((c) => c.label)).toEqual(["Settings › Executor"]);
  });
});

describe("announcements", () => {
  it("re-announces a repeated message by changing its text", () => {
    expect(nextAnnouncement("", "Saved.")).toBe("Saved.");
    expect(nextAnnouncement("Saved.", "Saved.")).not.toBe("Saved.");
    expect(nextAnnouncement("Saved.", "Applied.")).toBe("Applied.");
  });

  it("says a run started once, and nothing for a run already over", () => {
    expect(runStatusAnnouncement(null, { runId: "r1", live: true })).toBe("Agent run started.");
    expect(runStatusAnnouncement({ runId: "r1", live: true }, { runId: "r1", live: true })).toBe("");
    expect(runStatusAnnouncement(null, { runId: "r1", live: false })).toBe("");
    expect(runStatusAnnouncement({ runId: "r1", live: false }, { runId: "r2", live: true })).toBe("Agent run started.");
  });

  it("says an operation started (except an agent run, the panel's to say) and how it ended", () => {
    const label = (t: string) => ({ revise_plan: "Revising the plan", execute_initiative: "Executing the plan" })[t] ?? t;
    const plan = { id: "o1", type: "revise_plan" };
    const exec = { id: "o2", type: "execute_initiative" };
    expect(operationAnnouncement(null, plan, label, false)).toBe("Revising the plan started.");
    expect(operationAnnouncement(plan, plan, label, false)).toBe("");
    expect(operationAnnouncement(plan, null, label, false)).toBe("Revising the plan finished.");
    expect(operationAnnouncement(null, exec, label, false)).toBe("");
    expect(operationAnnouncement(exec, null, label, true)).toBe("Executing the plan stopped.");
    expect(operationAnnouncement(null, null, label, false)).toBe("");
  });
});
