// Entry point for the standalone API reference page (web/api-reference.html),
// served by the daemon at /api-reference.
//
// This file is intentionally not reachable from the React app: @scalar/api-reference
// is a Vue 3 application, and importing it from anywhere in the SPA's module
// graph would pull a second framework runtime into the app bundle. Vite builds
// it as its own entry (see vite.config.ts), so the two share nothing.
import { createApiReference } from "@scalar/api-reference";
import "@scalar/api-reference/style.css";

createApiReference("#app", {
  // The daemon serves the embedded spec here. It is outside /api/, so no token
  // is needed — the auth middleware guards /api/ only, and a fetch from this
  // page has no credential to offer.
  url: "/openapi.yaml",
  title: "idea-harness daemon API",

  // Scalar's default fonts are @font-face rules pointing at fonts.scalar.com
  // (not Google Fonts, as the plan assumed — same conclusion, different host).
  // The page is served with font-src 'self' data:, which blocks them outright,
  // so turning them off is the difference between "styled with system fonts"
  // and "styled with system fonts after a dozen blocked requests".
  withDefaultFonts: false,

  // Try-it is disabled rather than merely broken. Two independent reasons:
  // Scalar routes requests through https://proxy.scalar.com by default and
  // connect-src 'self' blocks it; and every endpoint under /api/ requires the
  // X-Idea-Token header, which this page deliberately does not have — so a
  // request that did go out would come back 401. Hiding the buttons is honest;
  // leaving them to fail silently is not.
  hideTestRequestButton: true,
  hideClientButton: true,
  proxyUrl: "",

  // "Agent Scalar" — the Ask AI chat — defaults to ON for local URLs:
  // ApiReference.vue enables it when `isLocalUrl(window.location.href)` is true
  // even with no API key, and this page is always on 127.0.0.1. Left alone it
  // would render an "Ask AI" input on a documentation page whose whole point is
  // that it works with no network. Off.
  agent: { disabled: true },

  // Same reasoning applied to every other host the bundle knows about: nothing
  // on this page should reach off-origin, and an empty base is inert.
  externalUrls: {
    proxyUrl: "",
    dashboardUrl: "",
    registryUrl: "",
    apiBaseUrl: "",
  },

  // "direct" links the download button straight at /openapi.yaml instead of
  // offering client-side YAML/JSON conversions of it; /openapi.json is the
  // daemon's own conversion and is the better JSON source.
  documentDownloadType: "direct",
});
