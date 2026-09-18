import { describe, expect, it } from "vitest";
import { daemonDownAfterPoll, daemonLostMessage } from "./daemonStatus";

describe("daemonDownAfterPoll", () => {
  it("is down when the single poll in the round was a network failure", () => {
    expect(daemonDownAfterPoll([{ networkError: true }])).toBe(true);
  });

  it("is down when any poll in a multi-request round was a network failure", () => {
    expect(
      daemonDownAfterPoll([{ networkError: false }, { networkError: true }, { networkError: false }]),
    ).toBe(true);
  });

  it("is up when every poll in the round succeeded", () => {
    expect(daemonDownAfterPoll([{ networkError: false }, { networkError: false }])).toBe(false);
  });

  it("is up (vacuously) for an empty round -- nothing was polled", () => {
    expect(daemonDownAfterPoll([])).toBe(false);
  });

  // M4 -- this is the reported defect: a single 404 or 500 (a response DID
  // come back) must never trip the daemon-down screen, only a request that
  // never got a response at all.
  it("is up for a round that got ordinary HTTP error responses, not network failures", () => {
    expect(
      daemonDownAfterPoll([{ networkError: false }, { networkError: false }, { networkError: false }]),
    ).toBe(false);
  });
});

describe("daemonLostMessage", () => {
  it("names the given host, not a hard-coded address", () => {
    expect(daemonLostMessage("example.com:9999")).toBe(
      "Lost connection to the daemon at example.com:9999.",
    );
  });

  it("does not mention 127.0.0.1:8787", () => {
    expect(daemonLostMessage("myhost:1234")).not.toContain("127.0.0.1");
  });
});
