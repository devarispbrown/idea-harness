// registerServiceWorker installs web/public/sw.js: the offline page, the
// hashed-asset cache and Web Push (see the header of sw.js for what it will
// never cache). Browsers only allow service workers in a secure context --
// HTTPS, or localhost -- so on a plain-HTTP remote address this is a no-op,
// and the app works exactly as it does without one.
export function shouldRegister(env: { secureContext: boolean; hasServiceWorker: boolean; dev: boolean }): boolean {
  // Not under `vite dev`: a worker caching /assets/ would fight hot reload.
  return env.secureContext && env.hasServiceWorker && !env.dev;
}

export function registerServiceWorker(): void {
  if (typeof window === "undefined") return;
  const ok = shouldRegister({
    secureContext: window.isSecureContext,
    hasServiceWorker: "serviceWorker" in navigator,
    dev: import.meta.env.DEV,
  });
  if (!ok) return;
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js", { scope: "/" }).catch(() => {
      /* the app works without it */
    });
  });
}
