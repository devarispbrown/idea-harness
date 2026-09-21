import { afterEach, describe, expect, it, vi } from "vitest";
import { entriesSince, getLastVisit, setLastVisit } from "./lastVisit";
import type { TimelineEntry } from "./timeline";

// fakeLocalStorage is a minimal, real (get/set actually round-trip) stand-in
// for window.localStorage, so the with-a-window suite below exercises the
// ACTUAL read/write path instead of only the no-window fallback.
function fakeLocalStorage(): Storage {
  const store = new Map<string, string>();
  return {
    getItem: (key: string) => store.get(key) ?? null,
    setItem: (key: string, value: string) => {
      store.set(key, value);
    },
    removeItem: (key: string) => {
      store.delete(key);
    },
    clear: () => store.clear(),
    key: (i: number) => Array.from(store.keys())[i] ?? null,
    get length() {
      return store.size;
    },
  } as Storage;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

function entry(at: string, title = "entry"): TimelineEntry {
  return { at, kind: "change_set_applied", title };
}

describe("entriesSince", () => {
  const entries = [entry("2026-09-18T12:00:00Z", "c"), entry("2026-09-17T12:00:00Z", "b"), entry("2026-09-16T12:00:00Z", "a")];

  it("keeps only entries strictly newer than the cursor", () => {
    const kept = entriesSince(entries, "2026-09-17T00:00:00Z");
    expect(kept.map((e) => e.title)).toEqual(["c", "b"]);
  });

  it("excludes an entry exactly at the cursor (strictly newer, not >=)", () => {
    const kept = entriesSince(entries, "2026-09-17T12:00:00Z");
    expect(kept.map((e) => e.title)).toEqual(["c"]);
  });

  it("returns nothing for a null cursor (first-ever visit) rather than everything", () => {
    expect(entriesSince(entries, null)).toEqual([]);
  });

  it("returns nothing for an unparseable cursor", () => {
    expect(entriesSince(entries, "not-a-date")).toEqual([]);
  });

  it("returns everything when the cursor predates all entries", () => {
    const kept = entriesSince(entries, "2000-01-01T00:00:00Z");
    expect(kept).toHaveLength(3);
  });

  it("returns nothing for an empty entries list", () => {
    expect(entriesSince([], "2000-01-01T00:00:00Z")).toEqual([]);
  });
});

// This suite runs under vitest's default "node" environment (no jsdom, no
// `window`, per api.ts's own module-level guard for the same reason) -- the
// point of these two is exactly that a missing/broken storage accessor must
// degrade quietly instead of throwing into the caller.
describe("getLastVisit / setLastVisit without a window", () => {
  it("getLastVisit returns null instead of throwing", () => {
    expect(() => getLastVisit("init-1")).not.toThrow();
    expect(getLastVisit("init-1")).toBeNull();
  });

  it("setLastVisit does not throw", () => {
    expect(() => setLastVisit("init-1", "2026-09-18T12:00:00Z")).not.toThrow();
  });
});

// LOW fix (release-gate report): the two tests above only prove the
// no-window fallback -- they'd still pass if getLastVisit/setLastVisit did
// nothing at all in the WITH-a-window case, since neither exercises it. This
// suite stubs a real `window.localStorage` (fakeLocalStorage actually
// stores and returns values) so the round-trip itself is asserted.
describe("getLastVisit / setLastVisit with a window", () => {
  it("setLastVisit then getLastVisit returns the stored value", () => {
    vi.stubGlobal("window", { localStorage: fakeLocalStorage() });
    setLastVisit("init-1", "2026-09-18T12:00:00Z");
    expect(getLastVisit("init-1")).toBe("2026-09-18T12:00:00Z");
  });

  it("keys are per-initiative -- one initiative's cursor never leaks into another's", () => {
    vi.stubGlobal("window", { localStorage: fakeLocalStorage() });
    setLastVisit("init-1", "2026-09-18T12:00:00Z");
    setLastVisit("init-2", "2026-09-19T00:00:00Z");
    expect(getLastVisit("init-1")).toBe("2026-09-18T12:00:00Z");
    expect(getLastVisit("init-2")).toBe("2026-09-19T00:00:00Z");
  });

  it("a later setLastVisit overwrites the same initiative's cursor", () => {
    vi.stubGlobal("window", { localStorage: fakeLocalStorage() });
    setLastVisit("init-1", "2026-09-18T12:00:00Z");
    setLastVisit("init-1", "2026-09-19T00:00:00Z");
    expect(getLastVisit("init-1")).toBe("2026-09-19T00:00:00Z");
  });

  it("getLastVisit is null for an initiative that was never stamped", () => {
    vi.stubGlobal("window", { localStorage: fakeLocalStorage() });
    expect(getLastVisit("never-visited")).toBeNull();
  });

  // A private-browsing quota, a blocked accessor, or a full quota all throw
  // synchronously from localStorage.setItem -- the exact failure mode the
  // module comment describes degrading from.
  it("setLastVisit swallows a throwing storage accessor rather than propagating it", () => {
    vi.stubGlobal("window", {
      localStorage: {
        setItem: () => {
          throw new DOMException("quota exceeded");
        },
      },
    });
    expect(() => setLastVisit("init-1", "2026-09-18T12:00:00Z")).not.toThrow();
  });

  it("getLastVisit swallows a throwing storage accessor and returns null", () => {
    vi.stubGlobal("window", {
      localStorage: {
        getItem: () => {
          throw new DOMException("blocked");
        },
      },
    });
    expect(getLastVisit("init-1")).toBeNull();
  });
});
