// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import RemoteRuns from "./RemoteRuns";
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
