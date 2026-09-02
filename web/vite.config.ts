import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { resolve } from "node:path";

export default defineConfig({
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
      input: {
        main: resolve(__dirname, "index.html"),
        "api-reference": resolve(__dirname, "api-reference.html"),
      },
    },
  },
});
