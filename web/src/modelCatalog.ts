// Pure model-list ordering/filtering for the Settings model picker (I),
// extracted so it is testable without a live provider call.
//
// CURATED_MODELS mirrors internal/model/catalog.go's DefaultModels. It is a
// duplicate, not a fetch: the picker needs a stable "lead with these" order
// even when a provider's live listing is unordered (OpenRouter returns ~450
// ids with no curation), and there is no endpoint that returns "which of
// these live ids are curated". ASSUMPTION for whoever edits
// internal/model/catalog.go next: this list needs the same edit, or the
// picker's ordering drifts from the CLI/API default. Enforced by
// internal/model/catalog_sync_test.go, which fails when the two differ.
export const CURATED_MODELS: Record<string, string[]> = {
  openai: [
    "gpt-5.6-luna",
    "gpt-5.6-sol",
    "gpt-5.6-terra",
    "gpt-5.5",
    "gpt-5.4-mini",
    "gpt-4.1",
    "gpt-4.1-mini",
    "gpt-4o",
    "gpt-4o-mini",
    "o3-mini",
  ],
  anthropic: [
    "claude-sonnet-5",
    "claude-opus-5",
    "claude-fable-5-1",
    "claude-haiku-4-5-20251001",
    "claude-opus-4-8",
    "claude-sonnet-4-5-20250929",
    "claude-sonnet-4-5",
  ],
  gemini: [
    "gemini-3.6-flash",
    "gemini-3.7-flash",
    "gemini-3.8-flash",
    "gemini-3.5-flash",
    "gemini-3.1-pro-preview",
    "gemini-2.5-flash",
    "gemini-2.5-pro",
  ],
  deepseek: ["deepseek-v4-pro", "deepseek-flash", "deepseek-chat", "deepseek-reasoner"],
  openrouter: [
    "anthropic/claude-sonnet-5",
    "openai/gpt-5.6-luna",
    "google/gemini-3.6-flash",
    "deepseek/deepseek-v4-pro-0813",
    "qwen/qwen3.8-max-0902",
    "moonshotai/kimi-k3",
    "z-ai/glm-5.3",
    "openai/gpt-4o",
    "meta-llama/llama-4-maverick",
  ],
  ollama: ["qwen3.8", "gpt-oss:20b", "gemma4:26b", "llama3.1", "llama3.2", "qwen2.5", "mistral", "deepseek-r1"],
};

// NON_CHAT_MARKERS are substrings that mark a model id as not a chat model:
// text-to-speech, embeddings, video/image generation, Q&A-only, and live/
// realtime-transcription endpoints all show up in provider listings
// alongside chat models but cannot serve as one (I2).
const NON_CHAT_MARKERS = [
  "tts",
  "embedding",
  "veo",
  "lyria",
  "aqa",
  "image",
  "transcribe",
  "live",
];

export function isChatModel(id: string): boolean {
  const lower = id.toLowerCase();
  return !NON_CHAT_MARKERS.some((marker) => lower.includes(marker));
}

// orderModels filters a provider's raw model ids down to chat models and
// puts the curated ones first, in catalog order, so a long unordered listing
// (OpenRouter) leads with sensible choices instead of whatever order the
// provider's API happened to return.
export function orderModels(provider: string, ids: string[]): string[] {
  const chatIds = ids.filter(isChatModel);
  const curated = CURATED_MODELS[provider] ?? [];
  const chatIdSet = new Set(chatIds);
  const curatedSet = new Set(curated);
  const head = curated.filter((m) => chatIdSet.has(m));
  const tail = chatIds.filter((m) => !curatedSet.has(m));
  return [...head, ...tail];
}

// chipsToShow bounds the picker's chip grid. With a search query typed, every
// match is shown (the query IS the bound). With none typed, only the first
// `cap` are rendered — curated models are always inside that window, since a
// curated list is a handful of ids and `cap` is generous — so OpenRouter's
// ~450 ids never render as one enormous unfiltered grid (I).
export function chipsToShow(ordered: string[], query: string, cap = 60): string[] {
  const q = query.trim().toLowerCase();
  if (q) {
    return ordered.filter((m) => m.toLowerCase().includes(q));
  }
  return ordered.length > cap ? ordered.slice(0, cap) : ordered;
}
