// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import RemoteRuns from "./RemoteRuns";
import RemoteRunsPage, { remoteRunsPath } from "./RemoteRunsPage";
import { fakeDaemon } from "./testing/fakeDaemon";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const path = "/api/v1/projects/p1/initiatives/i1/remote-runs";

describe("RemoteRuns", () => {
  it("loads the initiative's remote runs when opened", async () => {
    const { requests } = fakeDaemon({
      [`GET ${path}`]: [
        {
          id: "r2", task_code: "TASK-1", attempt: 2, server_url: "https://oh.example", conversation_id: "conv-2",
          status: "running", event_count: 3, last_event: "tool: npm test", updated_at: "2026-10-15T10:00:00Z",
        },
        {
          id: "r1", task_code: "TASK-1", attempt: 1, server_url: "https://oh.example", conversation_id: "conv-1",
          status: "failed", error: "server said 500", event_count: 0, updated_at: "2026-10-15T09:00:00Z",
        },
      ],
    });
    const { container } = render(<RemoteRuns projectId="p1" initiativeId="i1" />);
    // Nothing is fetched until the disclosure opens.
    expect(requests.length).toBe(0);
    const details = container.querySelector("details") as HTMLDetailsElement;
    details.open = true;
    fireEvent(details, new Event("toggle"));
    await waitFor(() => expect(screen.getByText("conv-2")).toBeTruthy());
    expect(requests[0]).toMatchObject({ method: "GET", path });
    expect(screen.getByText("server said 500")).toBeTruthy();
    expect(screen.getByText("tool: npm test")).toBeTruthy();
    expect(screen.getByText("(3 events)")).toBeTruthy();
  });

  it("says so when there are none", async () => {
    fakeDaemon({ [`GET ${path}`]: [] });
    const { container } = render(<RemoteRuns projectId="p1" initiativeId="i1" />);
    const details = container.querySelector("details") as HTMLDetailsElement;
    details.open = true;
    fireEvent(details, new Event("toggle"));
    await waitFor(() => expect(screen.getByText("No remote runs recorded for this initiative.")).toBeTruthy());
  });
});

describe("RemoteRunsPage", () => {
  const projectPath = "/api/v1/projects/p1/remote-runs";
  const rows = [
    {
      id: "r2", initiative_id: "i2", task_code: "TASK-3", attempt: 1, server_url: "https://oh.example", conversation_id: "conv-b",
      status: "running", event_count: 0, updated_at: "2026-10-15T10:00:00Z",
    },
    {
      id: "r1", initiative_id: "i1", task_code: "TASK-1", attempt: 1, server_url: "https://oh.example", conversation_id: "conv-a",
      status: "failed", error: "server said 500", event_count: 0, updated_at: "2026-10-15T09:00:00Z",
    },
  ];
  const initiatives = [
    { id: "i1", title: "A long recipe idea", short_title: "Recipes" },
    { id: "i2", title: "Meal plans" },
  ];

  it("lists every initiative's remote runs, each linking to its initiative's Build", async () => {
    const { requests } = fakeDaemon({ [`GET ${projectPath}`]: rows });
    render(<RemoteRunsPage projectId="p1" initiatives={initiatives} />);
    expect(screen.getByRole("heading", { level: 1, name: "Remote runs" })).toBeTruthy();
    await waitFor(() => expect(screen.getByText("conv-a")).toBeTruthy());
    expect(requests[0]).toMatchObject({ method: "GET", path: projectPath });
    expect(screen.getByRole("columnheader", { name: "Initiative" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Recipes" }).getAttribute("href")).toBe("#/i/i1/build");
    expect(screen.getByRole("link", { name: "Meal plans" }).getAttribute("href")).toBe("#/i/i2/build");
    expect(screen.getByText("server said 500")).toBeTruthy();
  });

  it("asks for only the runs not yet over when filtered", async () => {
    const { fetchMock } = fakeDaemon({ [`GET ${projectPath}`]: [] });
    render(<RemoteRunsPage projectId="p1" initiatives={initiatives} />);
    await waitFor(() => expect(screen.getByText(/No remote runs yet/)).toBeTruthy());
    fireEvent.click(screen.getByLabelText("Only runs not yet over"));
    await waitFor(() => expect(screen.getByText("No remote run is in progress.")).toBeTruthy());
    const urls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(urls.some((u) => u.endsWith("/remote-runs?active=true"))).toBe(true);
  });

  it("builds the listing path", () => {
    expect(remoteRunsPath("p 1", false)).toBe("/projects/p%201/remote-runs");
    expect(remoteRunsPath("p1", true)).toBe("/projects/p1/remote-runs?active=true");
  });
});
