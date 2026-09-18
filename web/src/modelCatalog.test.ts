import { describe, expect, it } from "vitest";
import { chipsToShow, isChatModel, orderModels } from "./modelCatalog";

describe("isChatModel", () => {
  it("accepts ordinary chat model ids", () => {
    expect(isChatModel("gpt-4o")).toBe(true);
    expect(isChatModel("claude-sonnet-5")).toBe(true);
  });

  it("rejects tts, embedding, image, video, qa and live ids", () => {
    for (const id of [
      "tts-1-hd",
      "text-embedding-3-large",
      "veo-3",
      "lyria-2",
      "aqa",
      "imagen-4",
      "gemini-live-2.5-flash",
      "whisper-transcribe-1",
    ]) {
      expect(isChatModel(id)).toBe(false);
    }
  });

  it("is case-insensitive", () => {
    expect(isChatModel("TTS-Standard")).toBe(false);
  });
});

describe("orderModels", () => {
  it("puts curated models first, in catalog order, regardless of live order", () => {
    const live = ["gpt-4o-mini", "gpt-5.6-luna", "some-new-model", "gpt-4o"];
    const ordered = orderModels("openai", live);
    // Catalog order is gpt-5.6-luna ... gpt-4o ... gpt-4o-mini, so both
    // curated ids must precede the uncatalogued one, in that relative order.
    expect(ordered.indexOf("gpt-5.6-luna")).toBeLessThan(ordered.indexOf("gpt-4o"));
    expect(ordered.indexOf("gpt-4o")).toBeLessThan(ordered.indexOf("gpt-4o-mini"));
    expect(ordered.indexOf("gpt-4o-mini")).toBeLessThan(ordered.indexOf("some-new-model"));
    expect(ordered).toContain("some-new-model");
  });

  it("drops non-chat ids from the result entirely", () => {
    const ordered = orderModels("openai", ["gpt-4o", "text-embedding-3-large", "tts-1"]);
    expect(ordered).toEqual(["gpt-4o"]);
  });

  it("only leads with curated ids the live list actually returned", () => {
    // openai's catalog leads with gpt-5.6-luna, but the live list here never
    // returned it — it must not be conjured into the result.
    const ordered = orderModels("openai", ["gpt-4o-mini"]);
    expect(ordered).toEqual(["gpt-4o-mini"]);
  });

  it("falls back to the live order for a provider with no curated list", () => {
    const ordered = orderModels("custom-provider", ["b-model", "a-model"]);
    expect(ordered).toEqual(["b-model", "a-model"]);
  });
});

describe("chipsToShow", () => {
  const many = Array.from({ length: 450 }, (_, i) => `model-${i}`);

  it("caps the unfiltered list", () => {
    expect(chipsToShow(many, "")).toHaveLength(60);
  });

  it("shows every match once a query is typed, even past the cap", () => {
    const ordered = ["model-1", "model-2", ...many.slice(100, 200)];
    const matches = chipsToShow(ordered, "model-1");
    // model-1, model-100..model-199 all contain "model-1"
    expect(matches.length).toBeGreaterThan(60);
  });

  it("does not cap a list already under the cap", () => {
    expect(chipsToShow(["a", "b", "c"], "")).toEqual(["a", "b", "c"]);
  });
});

describe("isChatModel on OpenAI's live listing", () => {
  it("drops audio, speech, realtime, research and moderation models", () => {
    for (const id of ["gpt-audio-2025", "whisper-1", "gpt-realtime", "o3-deep-research", "omni-moderation-latest", "dall-e-3", "gpt-4o-search-preview"]) {
      expect(isChatModel(id)).toBe(false);
    }
    expect(isChatModel("gpt-5.6-luna")).toBe(true);
  });
});
