// @vitest-environment jsdom
//
// Component tests for remote use (UI plan Phase 3): the project switcher, the
// sign-in page, the phone review of a change set, and the Settings panels.
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import ProjectSwitcher from "./ProjectSwitcher";
import LoginPage from "./LoginPage";
import BuildPage from "./BuildPage";
import NotificationsPanel from "./NotificationsPanel";
import RemoteAccessPanel from "./RemoteAccessPanel";
import { fakeDaemon } from "./testing/fakeDaemon";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
  window.localStorage.clear();
});

const P = "/api/v1/projects/p1";

describe("ProjectSwitcher", () => {
  const projects = [
    { project_id: "p1", name: "web" },
    { project_id: "p2", name: "api" },
  ];
  it("is a labelled select that switches", () => {
    const onSwitch = vi.fn();
    render(<ProjectSwitcher projects={projects} projectId="p1" onSwitch={onSwitch} />);
    const select = screen.getByLabelText("Project") as HTMLSelectElement;
    expect(select.value).toBe("p1");
    fireEvent.change(select, { target: { value: "p2" } });
    expect(onSwitch).toHaveBeenCalledWith("p2");
  });
  it("renders nothing with one project", () => {
    const { container } = render(<ProjectSwitcher projects={projects.slice(0, 1)} projectId="p1" onSwitch={() => {}} />);
    expect(container.innerHTML).toBe("");
  });
});

describe("LoginPage", () => {
  it("posts the code and signs in", async () => {
    const { requests } = fakeDaemon({ "POST /api/v1/ui/login": { auth: "session", csrf_token: "c" } });
    const onSignedIn = vi.fn();
    render(<LoginPage onSignedIn={onSignedIn} />);
    const submit = screen.getByRole("button", { name: "Sign in" }) as HTMLButtonElement;
    expect(submit.disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("Pairing code"), { target: { value: "ab12cde34f" } });
    expect((screen.getByLabelText("Pairing code") as HTMLInputElement).value).toBe("AB12C-DE34F");
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    await waitFor(() => expect(onSignedIn).toHaveBeenCalled());
    expect(requests[0]).toMatchObject({ method: "POST", path: "/api/v1/ui/login" });
    expect((requests[0].body as { code: string }).code).toBe("AB12C-DE34F");
  });
  it("says what to do when the code is refused", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(JSON.stringify({ error: "pairing code is invalid" }), { status: 401 })),
    );
    render(<LoginPage onSignedIn={() => {}} expired />);
    expect(screen.getByRole("status").textContent).toMatch(/signed out/);
    fireEvent.change(screen.getByLabelText("Pairing code"), { target: { value: "AAAAAAAAAA" } });
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect((await screen.findByRole("alert")).textContent).toMatch(/idea ui pair/);
  });
});

describe("BuildPage on a phone", () => {
  const cs = {
    id: "cs1",
    human_id: "CHG-1",
    task_id: "t1",
    status: "review_required",
    patch_digest: "abc",
    executor: "internal",
    review_completed_at: "2026-10-14T00:00:00Z",
  };
  const file = (path: string) => ({ path, change_type: "add", size: 10, patch: "@@ -0,0 +1 @@\n+line in " + path });

  it("collapses a long change set past the first files, wraps, and keeps the decision in one bar", async () => {
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("max-width"), media: q, addEventListener() {}, removeEventListener() {} }));
    const routes = {
      [`GET ${P}/initiatives/i1/change-sets`]: [cs],
      [`GET ${P}/change-sets/cs1/diff`]: { files: ["a.ts", "b.ts", "c.ts", "d.ts", "e.ts"].map(file), patch: "" },
      [`POST ${P}/change-sets/cs1/review`]: { status: "reviewed" },
    };
    const { requests } = fakeDaemon(routes);
    const { container } = render(<BuildPage projectId="p1" initiativeId="i1" />);
    await screen.findByText("CHG-1");
    fireEvent.click(screen.getByRole("button", { name: "View diff" }));
    await screen.findByText("a.ts");
    // Wrapping is on by default on a narrow screen.
    expect((screen.getByLabelText("Wrap long lines") as HTMLInputElement).checked).toBe(true);
    expect(container.querySelector(".cs-files.cs-diff-wrap")).toBeTruthy();
    // The first two files are open; the rest start collapsed.
    expect(screen.getByRole("button", { name: "Hide a.ts" }).getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByRole("button", { name: "Show e.ts" }).getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText(/line in e.ts/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Show e.ts" }));
    expect(screen.getByText(/line in e.ts/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Expand all files" }));
    fireEvent.click(screen.getByRole("button", { name: "Collapse all files" }));
    expect(screen.queryByText(/line in a.ts/)).toBeNull();

    // The actions and the note live in the decision bar (sticky on a phone).
    const bar = container.querySelector(".cs-decision-bar")!;
    expect(bar.querySelector("button")?.textContent).toBe("Approve");
    fireEvent.click(screen.getByRole("button", { name: "Request changes…" }));
    expect(bar.querySelector("textarea")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("What should change in CHG-1?"), { target: { value: "not this" } });
    fireEvent.click(screen.getByRole("button", { name: "Reject with this note" }));
    await waitFor(() => expect(requests.some((r) => r.method === "POST")).toBe(true));
    expect(requests.find((r) => r.method === "POST")!.body).toEqual({ verdict: "rejected", note: "not this" });
  });
});

describe("Settings panels", () => {
  it("Notifications explains why push is unavailable on an insecure page and offers no button", async () => {
    fakeDaemon({ [`GET ${P}/push/subscriptions`]: { subscriptions: [], event_kinds: [] } });
    // jsdom is not a secure context and has no PushManager.
    render(<NotificationsPanel projectId="p1" />);
    expect(await screen.findByText(/Notifications need a secure page/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Turn on notifications/ })).toBeNull();
  });

  it("Notifications toggles one event kind for a subscribed device", async () => {
    const sub = {
      id: "s1",
      label: "iPhone",
      endpoint_host: "web.push.apple.com",
      events: ["initiative.needs_judgment", "verify.failed"],
      created_at: "2026-10-14T00:00:00Z",
    };
    const kinds = ["initiative.needs_judgment", "change_set.ready_for_review", "verify.failed", "outcome.drifted"];
    const { requests } = fakeDaemon({
      [`GET ${P}/push/subscriptions`]: { subscriptions: [sub], event_kinds: kinds },
      [`PATCH ${P}/push/subscriptions/s1`]: sub,
    });
    render(<NotificationsPanel projectId="p1" />);
    const box = (await screen.findByLabelText("A change set is ready for review")) as HTMLInputElement;
    expect(box.checked).toBe(false);
    fireEvent.click(box);
    await waitFor(() => expect(requests.some((r) => r.method === "PATCH")).toBe(true));
    expect(requests.find((r) => r.method === "PATCH")!.body).toEqual({
      events: ["initiative.needs_judgment", "change_set.ready_for_review", "verify.failed"],
    });
  });

  it("Remote access lists live sessions and revokes one", async () => {
    const future = new Date(Date.now() + 86400000).toISOString();
    const { requests } = fakeDaemon({
      "GET /api/v1/ui/sessions": {
        sessions: [
          { id: "u1", label: "Pixel", created_at: future, last_seen_at: future, expires_at: future },
          { id: "u2", label: "Old", created_at: future, last_seen_at: future, expires_at: future, revoked_at: future },
        ],
        current: "",
      },
      "DELETE /api/v1/ui/sessions/u1": {},
    });
    render(<RemoteAccessPanel />);
    expect(await screen.findByText("Pixel")).toBeTruthy();
    expect(screen.queryByText("Old")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Revoke" }));
    await waitFor(() => expect(requests.some((r) => r.method === "DELETE")).toBe(true));
    expect(screen.queryByText(/burned/)).toBeNull();
  });

  it("Remote access says when pairing codes were burned", async () => {
    fakeDaemon({
      "GET /api/v1/ui/sessions": { sessions: [], current: "", burned_codes: 2, last_burned_at: new Date().toISOString() },
    });
    render(<RemoteAccessPanel />);
    expect(await screen.findByText(/2 pairing codes were burned/)).toBeTruthy();
  });
});
