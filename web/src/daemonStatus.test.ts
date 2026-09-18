import { describe, expect, it } from "vitest";
import { daemonDownAfterPoll, daemonLostMessage } from "./daemonStatus";

describe("daemonDownAfterPoll", () => {
  it("is down when the single poll in the round failed", () => {
    expect(daemonDownAfterPoll([{ ok: false }])).toBe(true);
  });

  it("is down when any poll in a multi-request round failed", () => {
    expect(daemonDownAfterPoll([{ ok: true }, { ok: false }, { ok: true }])).toBe(true);
  });

  it("is up when every poll in the round succeeded", () => {
    expect(daemonDownAfterPoll([{ ok: true }, { ok: true }])).toBe(false);
  });

  it("is up (vacuously) for an empty round -- nothing was polled", () => {
    expect(daemonDownAfterPoll([])).toBe(false);
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
