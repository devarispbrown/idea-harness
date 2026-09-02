import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { resolve } from "node:path";

// APIDOCS=0 builds without the bundled API reference (~3.7 MiB of binary).
// Anything else, including unset, keeps it: nobody should get a smaller binary
// by accident.
const apiDocsEnabled = process.env.APIDOCS !== "0";

// API_REFERENCE_BASE sets the base for the built page's asset URLs. The default
// "/" is what the daemon serves (GET /api-reference, no trailing slash, so the
// document base is the site root). `make api-docs-dist` overrides it with "./"
// so a CDN can serve the page from a subpath — without it the page requests
// /assets/... absolutely and 404s anywhere but the site root.
const base = process.env.API_REFERENCE_BASE || "/";

export default defineConfig({
  base,
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/api": "http://127.0.0.1:8787",
      // The API reference page fetches the spec from the daemon, which serves it
      // outside /api/ (the token middleware guards only /api/). Proxy it too, so
      // /api-reference.html works under `pnpm dev` and not only after a build.
      "/openapi.yaml": "http://127.0.0.1:8787",
      "/openapi.json": "http://127.0.0.1:8787",
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    rollupOptions: {
      // Two entry points, not one. @scalar/api-reference is a Vue 3 application;
      // mounting it inside the React SPA would ship a second framework runtime to
      // every UI visitor. A separate HTML entry gives it its own chunk graph, so
      // the app bundle is untouched and the reference is only downloaded by
      // someone who opens /api-reference.
      // APIDOCS=0 drops the reference entry, which is the only lever that
      // shrinks the binary: internal/web embeds dist/ with `//go:embed all:dist`,
      // and embed patterns are compile-time literals, so no Go build tag can
      // strip individual files back out. The assets have to not be built.
      input: apiDocsEnabled
        ? {
            main: resolve(__dirname, "index.html"),
            "api-reference": resolve(__dirname, "api-reference.html"),
          }
        : { main: resolve(__dirname, "index.html") },
    },
  },
});
