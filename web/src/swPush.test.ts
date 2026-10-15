/// <reference types="vite/client" />
import { describe, expect, it } from "vitest";
import swPushSource from "../public/sw-push.js?raw";
import { RENEWAL_DB, RENEWAL_DB_VERSION, RENEWAL_STORE, type RenewalRecord } from "./pushRenewalStore";

// The worker's renewal logic (web/public/sw-push.js) is a classic script that
// defines self.IdeaPushRenewal. It is run here exactly as shipped, against a
// fake `self`, with the store, push manager and fetch injected.

interface RenewResult {
  projectId: string;
  result: string;
  status?: number;
}
interface RenewalApi {
  DB_NAME: string;
  DB_VERSION: number;
  STORE: string;
  renewAll(event: unknown, deps: unknown): Promise<RenewResult[]>;
}

function loadWorkerModule(): RenewalApi {
  const fakeSelf: { IdeaPushRenewal?: RenewalApi } = {};
  new Function("self", swPushSource)(fakeSelf);
  return fakeSelf.IdeaPushRenewal!;
}

function memoryStore(initial: RenewalRecord[]) {
  const rows = new Map(initial.map((r) => [r.projectId, r]));
  return {
    rows,
    all: async () => [...rows.values()],
    put: async (r: RenewalRecord) => void rows.set(r.projectId, r),
    remove: async (id: string) => void rows.delete(id),
  };
}

function fakeSub(endpoint: string) {
  return { toJSON: () => ({ endpoint, keys: { p256dh: "P", auth: "A" } }) };
}

type Call = { url: string; init: RequestInit };
function fakeFetch(answers: Record<string, { status: number; body?: unknown } | "offline">) {
  const calls: Call[] = [];
  const fn = async (url: string, init: RequestInit) => {
    calls.push({ url, init });
    const a = answers[url];
    if (!a || a === "offline") throw new TypeError("Failed to fetch");
    return { ok: a.status >= 200 && a.status < 300, status: a.status, json: async () => a.body };
  };
  return { calls, fn };
}

const rec = (projectId: string, subscriptionId: string, token: string): RenewalRecord => ({
  projectId,
  subscriptionId,
  renewalToken: token,
  vapidKey: "BPgx-OIa0yYPIJV1sbxlhRx3lBXOaQ3D1v6yQ8m8bYQ",
});
const renewURL = (p: string, s: string) => `/api/v1/projects/${p}/push/subscriptions/${s}/renew`;

describe("service worker push renewal", () => {
  const api = loadWorkerModule();

  it("shares its IndexedDB names with the page", () => {
    expect([api.DB_NAME, api.DB_VERSION, api.STORE]).toEqual([RENEWAL_DB, RENEWAL_DB_VERSION, RENEWAL_STORE]);
  });

  it("re-points every project's row at the new subscription and keeps the rotated tokens", async () => {
    const store = memoryStore([rec("p1", "s1", "t1"), rec("p2", "s2", "t2")]);
    const f = fakeFetch({
      [renewURL("p1", "s1")]: { status: 200, body: { id: "s1", renewal_token: "t1b" } },
      // Folded into an existing row: the id changes.
      [renewURL("p2", "s2")]: { status: 200, body: { id: "s9", renewal_token: "t2b" } },
    });
    const out = await api.renewAll({ newSubscription: fakeSub("https://push.example/new") }, { store, fetch: f.fn, pushManager: {} });
    expect(out.map((r) => r.result)).toEqual(["renewed", "renewed"]);
    expect(store.rows.get("p1")).toMatchObject({ subscriptionId: "s1", renewalToken: "t1b" });
    expect(store.rows.get("p2")).toMatchObject({ subscriptionId: "s9", renewalToken: "t2b", vapidKey: rec("", "", "").vapidKey });

    // What goes over the wire: the token in the body, never in the URL, and
    // no cookie -- the token is the credential.
    const body = JSON.parse(String(f.calls[0].init.body));
    expect(body).toEqual({ renewal_token: "t1", endpoint: "https://push.example/new", keys: { p256dh: "P", auth: "A" } });
    expect(f.calls[0].url).not.toContain("t1");
    expect(f.calls[0].init.credentials).toBe("omit");
    expect(f.calls[0].init.method).toBe("POST");
    expect((f.calls[0].init.headers as Record<string, string>)["Content-Type"]).toBe("application/json");
  });

  it("subscribes again with the old subscription's VAPID key when the browser supplies no new one", async () => {
    const store = memoryStore([rec("p1", "s1", "t1")]);
    const subscribed: unknown[] = [];
    const key = new Uint8Array([4, 1, 2]);
    const pushManager = {
      getSubscription: async () => null,
      subscribe: async (opts: unknown) => {
        subscribed.push(opts);
        return fakeSub("https://push.example/fresh");
      },
    };
    const f = fakeFetch({ [renewURL("p1", "s1")]: { status: 200, body: { id: "s1", renewal_token: "t1b" } } });
    await api.renewAll({ oldSubscription: { options: { applicationServerKey: key } } }, { store, fetch: f.fn, pushManager });
    expect(subscribed).toEqual([{ userVisibleOnly: true, applicationServerKey: key }]);
    expect(JSON.parse(String(f.calls[0].init.body)).endpoint).toBe("https://push.example/fresh");
  });

  it("falls back to the stored VAPID key, and to a subscription another tab already made", async () => {
    const store = memoryStore([rec("p1", "s1", "t1")]);
    let subscribeArgs: { applicationServerKey: Uint8Array } | undefined;
    const pushManager = {
      getSubscription: async () => null,
      subscribe: async (opts: { applicationServerKey: Uint8Array }) => {
        subscribeArgs = opts;
        return fakeSub("https://push.example/fresh");
      },
    };
    const f = fakeFetch({ [renewURL("p1", "s1")]: { status: 200, body: { id: "s1", renewal_token: "t1b" } } });
    await api.renewAll({}, { store, fetch: f.fn, pushManager });
    // base64url decoded: 32 bytes for this 43-character key.
    expect(subscribeArgs?.applicationServerKey).toBeInstanceOf(Uint8Array);
    expect(subscribeArgs?.applicationServerKey.length).toBe(32);

    const already = { getSubscription: async () => fakeSub("https://push.example/other-tab"), subscribe: async () => fakeSub("x") };
    const f2 = fakeFetch({ [renewURL("p1", "s1")]: { status: 200, body: { id: "s1", renewal_token: "t1c" } } });
    store.rows.set("p1", rec("p1", "s1", "t1b"));
    await api.renewAll({}, { store, fetch: f2.fn, pushManager: already });
    expect(JSON.parse(String(f2.calls[0].init.body)).endpoint).toBe("https://push.example/other-tab");
  });

  it("forgets a row the daemon refuses, and keeps one it could not reach", async () => {
    const store = memoryStore([rec("gone", "s1", "t1"), rec("down", "s2", "t2"), rec("offline", "s3", "t3")]);
    const f = fakeFetch({
      [renewURL("gone", "s1")]: { status: 401 },
      [renewURL("down", "s2")]: { status: 503 },
      [renewURL("offline", "s3")]: "offline",
    });
    const out = await api.renewAll({ newSubscription: fakeSub("https://push.example/new") }, { store, fetch: f.fn, pushManager: {} });
    expect(out).toEqual([
      { projectId: "gone", result: "forgotten" },
      { projectId: "down", result: "kept", status: 503 },
      { projectId: "offline", result: "kept" },
    ]);
    expect([...store.rows.keys()]).toEqual(["down", "offline"]);
    expect(store.rows.get("down")?.renewalToken).toBe("t2");
  });

  it("does nothing when this browser never turned notifications on", async () => {
    const f = fakeFetch({});
    const out = await api.renewAll({ newSubscription: fakeSub("https://push.example/new") }, { store: memoryStore([]), fetch: f.fn, pushManager: {} });
    expect(out).toEqual([]);
    expect(f.calls).toHaveLength(0);
  });

  it("escapes ids into the path", async () => {
    const store = memoryStore([rec("a/b", "c?d", "t")]);
    const f = fakeFetch({ [renewURL("a%2Fb", "c%3Fd")]: { status: 200, body: { id: "c?d", renewal_token: "t2" } } });
    const out = await api.renewAll({ newSubscription: fakeSub("https://push.example/new") }, { store, fetch: f.fn, pushManager: {} });
    expect(out[0].result).toBe("renewed");
  });
});
