import { describe, expect, it } from "vitest";
import {
  attentionChecks,
  citationLabel,
  enabledRows,
  gateLabel,
  offDimensions,
  statusLabel,
  statusTone,
  type QualityCheck,
  type QualityDimensionRow,
  type QualityResult,
  type QualityScorecard,
} from "./quality";

const result = (status: string, checks: QualityCheck[] = []): QualityResult => ({
  id: "q",
  status,
  summary: "",
  checks,
  requirements: [],
});

const row = (dimension: string, mode: string, r: QualityResult | null, stale = false): QualityDimensionRow => ({
  dimension,
  mode,
  result: r,
  stale,
});

describe("quality scorecard", () => {
  it("never shows an unmeasured or stale dimension as its last verdict", () => {
    expect(statusLabel(row("docs_completeness", "gate", null))).toBe("not measured");
    expect(statusTone(row("docs_completeness", "gate", null))).toBe("warn");
    expect(statusLabel(row("docs_completeness", "gate", result("pass"), true))).toBe("pass (stale)");
    expect(statusTone(row("docs_completeness", "gate", result("pass"), true))).toBe("warn");
    expect(statusTone(row("security", "advisory", result("not_measured")))).toBe("warn");
    expect(statusTone(row("security", "advisory", result("pass")))).toBe("ok");
    expect(statusTone(row("security", "advisory", result("fail")))).toBe("bad");
  });

  it("splits enabled from off and counts only gated dimensions in the verdict", () => {
    const sc: QualityScorecard = {
      rigor: "standard",
      overridden: [],
      needs_measurement: false,
      gate_unmet: [],
      dimensions: [row("setup_reproducibility", "advisory", result("fail")), row("performance", "off", null)],
    };
    expect(enabledRows(sc).map((r) => r.dimension)).toEqual(["setup_reproducibility"]);
    expect(offDimensions(sc)).toEqual(["performance"]);
    expect(gateLabel(sc)).toMatch(/advisory only/);
    sc.dimensions[0].mode = "gate";
    sc.gate_unmet = ["setup_reproducibility fail"];
    expect(gateLabel(sc)).toBe("Gate unmet: setup_reproducibility fail");
    sc.gate_unmet = [];
    expect(gateLabel(sc)).toBe("All 1 gated dimension met");
  });

  it("lists only checks that need attention, with their citation", () => {
    const r = result("fail", [
      { name: "readme", status: "pass", method: "deterministic" },
      { name: "openapi", status: "not_applicable", method: "deterministic" },
      { name: "routes_documented", status: "fail", method: "deterministic", citations: [{ path: "main.go", line: 9 }] },
      { name: "go_api", status: "warn", method: "deterministic", citations: [{ path: "lib.go", line: 3, requirement: "REQ-2" }] },
    ]);
    const att = attentionChecks(r);
    expect(att.map((c) => c.name)).toEqual(["routes_documented", "go_api"]);
    expect(citationLabel(att[0])).toBe("main.go:9");
    expect(citationLabel(att[1])).toBe("lib.go:3 (REQ-2)");
    expect(attentionChecks(null)).toEqual([]);
  });
});
