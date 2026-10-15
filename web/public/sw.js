// idea-harness service worker: an offline shell and Web Push, nothing else.
//
// What it caches, and why so little:
//
//   - offline.html and the app icons, precached: the page shown when the
//     daemon cannot be reached, instead of the browser's own error.
//   - /assets/*, on first use: Vite's content-hashed JS and CSS. Immutable,
//     carry no data and no credential.
//
// What it NEVER caches: anything under /api/ (every product-graph read, and
// every secret the redactor did not recognise, comes through there), and the
// app's own HTML -- on loopback, index.html carries the API token. Those
// requests are not even intercepted: the browser handles them exactly as it
// would without a service worker. A stale UI is a cheap failure; a cached
// diff or token on a lost phone is not.
//
// Push: the daemon (internal/integrations/push) sends a small JSON payload --
// title, body, a same-origin URL -- encrypted to this browser (RFC 8291). It
// is shown as a notification; clicking it focuses an open tab on that URL or
// opens one.

//
// Renewal: when the push service replaces this browser's subscription, the
// worker re-points the daemon's rows at the new one (sw-push.js).

importScripts("/sw-push.js");

const SHELL = "idea-harness-shell-v1";
const ASSETS = "idea-harness-assets-v1";
const PRECACHE = ["/offline.html", "/icons/icon-192.png", "/icons/icon-512.png", "/manifest.webmanifest"];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches
      .open(SHELL)
      .then((c) => c.addAll(PRECACHE))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== SHELL && k !== ASSETS).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  if (req.method !== "GET") return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  if (url.pathname.startsWith("/api/")) return; // never touched

  if (req.mode === "navigate") {
    // Network only; the offline page when there is no network. The response
    // itself is never stored.
    event.respondWith(fetch(req).catch(() => caches.match("/offline.html")));
    return;
  }
  if (url.pathname.startsWith("/assets/")) {
    event.respondWith(
      caches.open(ASSETS).then(async (cache) => {
        const hit = await cache.match(req);
        if (hit) return hit;
        const res = await fetch(req);
        if (res.ok) cache.put(req, res.clone());
        return res;
      }),
    );
  }
});

self.addEventListener("push", (event) => {
  let data = {};
  try {
    data = event.data ? event.data.json() : {};
  } catch {
    data = { title: "idea-harness", body: event.data ? event.data.text() : "" };
  }
  const title = data.title || "idea-harness";
  event.waitUntil(
    self.registration.showNotification(title, {
      body: data.body || "",
      tag: data.tag || undefined,
      icon: "/icons/icon-192.png",
      badge: "/icons/icon-192.png",
      data: { url: sameOrigin(data.url) },
    }),
  );
});

self.addEventListener("pushsubscriptionchange", (event) => {
  event.waitUntil(
    self.IdeaPushRenewal.renewAll(event, {
      store: self.IdeaPushRenewal.idbStore(self.indexedDB),
      pushManager: self.registration.pushManager,
      fetch: (url, init) => fetch(url, init),
    }),
  );
});

// sameOrigin keeps a notification from opening anything but this app: the
// payload is the daemon's, but a click must never be a way off-site.
function sameOrigin(u) {
  try {
    const url = new URL(u || "/", self.location.origin);
    return url.origin === self.location.origin ? url.pathname + url.search + url.hash : "/";
  } catch {
    return "/";
  }
}

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const target = (event.notification.data && event.notification.data.url) || "/";
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((wins) => {
      for (const w of wins) {
        if (new URL(w.url).origin === self.location.origin && "focus" in w) {
          return w.focus().then((c) => (c && "navigate" in c ? c.navigate(target) : c));
        }
      }
      return self.clients.openWindow(target);
    }),
  );
});
