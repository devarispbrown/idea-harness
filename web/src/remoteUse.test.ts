import { afterEach, describe, expect, it, vi } from "vitest";
import { parseRoute, splitProject, withProject } from "./router";
import { pickProject, projectLabel, rememberProject, rememberedProject, LAST_PROJECT_KEY } from "./projectSwitch";
import { loginErrorMessage, normalizeCode, resolveAuth, login, deviceName } from "./remote";
import { authHeaders, setCsrfToken } from "./api";
import {
  pushUnavailableReason,
  storePushId,
  storedPushId,
  toggleEvent,
  urlBase64ToUint8Array,
  type PushEnv,
} from "./push";
import { COLLAPSE_AFTER, diffStat, initialCollapsed, isNarrow, toggled } from "./mobileLayout";
import { shouldRegister } from "./serviceWorker";

afterEach(() => {
  setCsrfToken("");
  vi.unstubAllGlobals();
});

describe("project prefix in the hash", () => {
  it("splits #/p/<id>/… from the route underneath", () => {
    expect(splitProject("#/p/abc/i/x/build")).toEqual({ projectId: "abc", rest: "#/i/x/build" });
    expect(splitProject("#/p/abc")).toEqual({ projectId: "abc", rest: "#/" });
    expect(splitProject("#/p/abc/")).toEqual({ projectId: "abc", rest: "#/" });
    expect(splitProject("#/i/x/build")).toEqual({ projectId: null, rest: "#/i/x/build" });
    expect(splitProject("")).toEqual({ projectId: null, rest: "#" });
    expect(splitProject("#/p/a%20b/queue").projectId).toBe("a b");
  });

  it("parses the same route with or without the prefix", () => {
    expect(parseRoute("#/p/abc/i/x/build")).toEqual(parseRoute("#/i/x/build"));
    expect(parseRoute("#/p/abc/queue")).toEqual({ name: "queue" });
    expect(parseRoute("#/p/abc/")).toEqual({ name: "home" });
    // "p" alone is not a project prefix, and stays not-found.
    expect(parseRoute("#/p")).toEqual({ name: "not-found", path: "p" });
  });

  it("prefixes in-app links with a project, once", () => {
    expect(withProject("#/i/x/build", "abc")).toBe("#/p/abc/i/x/build");
    expect(withProject("#/", "abc")).toBe("#/p/abc/");
    expect(withProject("#/p/other/queue", "abc")).toBe("#/p/other/queue");
    expect(withProject("#/i/x", "")).toBe("#/i/x");
    expect(withProject("https://example.com/", "abc")).toBe("https://example.com/");
    expect(withProject("#/q", "a/b")).toBe("#/p/a%2Fb/q");
  });
});

describe("pickProject", () => {
  const ps = [
    { project_id: "p1", name: "web" },
    { project_id: "p2", name: "api" },
  ];
  it("prefers the address, then the remembered choice, then the first project", () => {
    expect(pickProject(ps, "p2", "p1")).toEqual({ projectId: "p2" });
    expect(pickProject(ps, null, "p2")).toEqual({ projectId: "p2" });
    expect(pickProject(ps, null, "gone")).toEqual({ projectId: "p1" });
    expect(pickProject(ps, null, null)).toEqual({ projectId: "p1" });
  });
  it("reports a project the address names but the daemon does not serve", () => {
    expect(pickProject(ps, "nope", "p2")).toEqual({ projectId: "p1", unknown: "nope" });
  });
  it("handles no projects", () => {
    expect(pickProject([], null, null)).toEqual({ projectId: "" });
  });
  it("remembers through storage and never throws when storage does", () => {
    const store = new Map<string, string>();
    const storage = { setItem: (k: string, v: string) => store.set(k, v), getItem: (k: string) => store.get(k) ?? null };
    rememberProject("p2", storage);
    expect(store.get(LAST_PROJECT_KEY)).toBe("p2");
    expect(rememberedProject(storage)).toBe("p2");
    const broken = {
      setItem: () => {
        throw new Error("quota");
      },
      getItem: () => {
        throw new Error("denied");
      },
    };
    expect(() => rememberProject("p1", broken)).not.toThrow();
    expect(rememberedProject(broken)).toBeNull();
  });
  it("disambiguates two projects with the same name", () => {
    const twins = [
      { project_id: "aaaaaa1111", name: "app" },
      { project_id: "bbbbbb2222", name: "app" },
      { project_id: "cccccc3333", name: "docs" },
    ];
    expect(projectLabel(twins[0], twins)).toBe("app (aaaaaa)");
    expect(projectLabel(twins[2], twins)).toBe("docs");
  });
});

describe("remote sign-in helpers", () => {
  it("normalizes a typed code to XXXXX-XXXXX", () => {
    expect(normalizeCode("ab12c")).toBe("AB12C");
    expect(normalizeCode("ab12cde34f")).toBe("AB12C-DE34F");
    expect(normalizeCode(" ab12c-de34f ")).toBe("AB12C-DE34F");
    expect(normalizeCode("ab12cde34fZZZ")).toBe("AB12C-DE34F");
  });
  it("explains each sign-in failure with what to do", () => {
    expect(loginErrorMessage(401, "")).toMatch(/idea ui pair/);
    expect(loginErrorMessage(429, "")).toMatch(/15 minutes/);
    // The daemon's own 429 says whether only this device is paused.
    expect(loginErrorMessage(429, "too many failed sign-ins from this device: wait 3 minute(s)")).toMatch(/this device/);
    expect(loginErrorMessage(403, "")).toMatch(/directly/);
    expect(loginErrorMessage(500, "boom")).toBe("boom");
  });
  it("names devices", () => {
    expect(deviceName("Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)")).toBe("iPhone");
    expect(deviceName("Mozilla/5.0 (Linux; Android 14)")).toBe("Android");
    expect(deviceName("")).toBe("Browser");
  });

  it("resolveAuth: a live session keeps its CSRF token; 401 is signed out", async () => {
    const ok = vi.fn(async () => new Response(JSON.stringify({ auth: "session", csrf_token: "c1" }), { status: 200 }));
    expect(await resolveAuth(ok as unknown as typeof fetch)).toBe("session");
    expect(authHeaders("POST")).toEqual({ "X-Idea-CSRF": "c1" });
    expect(authHeaders("GET")).toEqual({});
    const out = vi.fn(async () => new Response(JSON.stringify({ error: "unauthorized" }), { status: 401 }));
    expect(await resolveAuth(out as unknown as typeof fetch)).toBe("signed-out");
    const down = vi.fn(async () => {
      throw new TypeError("network");
    });
    expect(await resolveAuth(down as unknown as typeof fetch)).toBe("unreachable");
  });

  it("login posts the code and keeps the CSRF token, never a token in a URL", async () => {
    const fetchFn = vi.fn(async (_url: string, init?: RequestInit) => {
      expect(JSON.parse(String(init?.body))).toEqual({ code: "AB12C-DE34F", label: "iPhone" });
      return new Response(JSON.stringify({ auth: "session", csrf_token: "c2" }), { status: 200 });
    });
    expect(await login("AB12C-DE34F", "iPhone", fetchFn as unknown as typeof fetch)).toEqual({ ok: true });
    expect(fetchFn.mock.calls[0][0]).toBe("/api/v1/ui/login");
    expect(authHeaders("DELETE")).toEqual({ "X-Idea-CSRF": "c2" });
    const bad = vi.fn(async () => new Response(JSON.stringify({ error: "pairing code is invalid" }), { status: 401 }));
    const res = await login("x", "", bad as unknown as typeof fetch);
    expect(res.ok).toBe(false);
  });
});

describe("push helpers", () => {
  const env = (o: Partial<PushEnv>): PushEnv => ({
    secureContext: true,
    serviceWorker: true,
    pushManager: true,
    notification: true,
    permission: "default",
    ...o,
  });
  it("says why push is unavailable, most actionable first", () => {
    expect(pushUnavailableReason(env({}))).toBe("");
    expect(pushUnavailableReason(env({ secureContext: false }))).toMatch(/HTTPS/);
    expect(pushUnavailableReason(env({ pushManager: false }))).toMatch(/Home Screen/);
    expect(pushUnavailableReason(env({ permission: "denied" }))).toMatch(/blocked/);
  });
  it("decodes the VAPID key", () => {
    // 0x04 followed by 0xff 0xfe: base64url "BP_-"
    expect(Array.from(urlBase64ToUint8Array("BP_-"))).toEqual([4, 255, 254]);
    expect(urlBase64ToUint8Array("A".repeat(87)).length).toBe(65);
  });
  it("toggles an event in canonical order", () => {
    const order = ["initiative.needs_judgment", "change_set.ready_for_review", "verify.failed", "outcome.drifted"];
    expect(toggleEvent(["verify.failed"], "initiative.needs_judgment", order)).toEqual(["initiative.needs_judgment", "verify.failed"]);
    expect(toggleEvent(["verify.failed"], "verify.failed", order)).toEqual([]);
  });
  it("stores this browser's subscription id per project", () => {
    const m = new Map<string, string>();
    const s = { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), removeItem: (k: string) => void m.delete(k) };
    storePushId("p1", "sub1", s);
    expect(storedPushId("p1", s)).toBe("sub1");
    expect(storedPushId("p2", s)).toBe("");
    storePushId("p1", "", s);
    expect(storedPushId("p1", s)).toBe("");
  });
});

describe("phone layout", () => {
  it("collapses all but the first files of a long change set on a phone only", () => {
    const paths = ["a", "b", "c", "d", "e"];
    expect(initialCollapsed(paths, false).size).toBe(0);
    expect([...initialCollapsed(paths, true)]).toEqual(paths.slice(COLLAPSE_AFTER));
    expect(initialCollapsed(paths.slice(0, COLLAPSE_AFTER + 1), true).size).toBe(0);
  });
  it("narrow is 760px and below", () => {
    expect(isNarrow(390)).toBe(true);
    expect(isNarrow(760)).toBe(true);
    expect(isNarrow(1280)).toBe(false);
  });
  it("toggles a path without mutating", () => {
    const a = new Set(["x"]);
    const b = toggled(a, "x");
    expect(a.has("x")).toBe(true);
    expect(b.has("x")).toBe(false);
    expect(toggled(b, "y").has("y")).toBe(true);
  });
  it("counts a patch's added and removed lines, not its headers", () => {
    expect(diffStat("--- a/x\n+++ b/x\n@@ -1 +1,2 @@\n-old\n+new\n+more\n same")).toEqual({ added: 2, removed: 1 });
  });
});

describe("service worker registration", () => {
  it("registers only in a secure context, outside vite dev", () => {
    expect(shouldRegister({ secureContext: true, hasServiceWorker: true, dev: false })).toBe(true);
    expect(shouldRegister({ secureContext: false, hasServiceWorker: true, dev: false })).toBe(false);
    expect(shouldRegister({ secureContext: true, hasServiceWorker: false, dev: false })).toBe(false);
    expect(shouldRegister({ secureContext: true, hasServiceWorker: true, dev: true })).toBe(false);
  });
});
