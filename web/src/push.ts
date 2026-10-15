// Web Push in the browser (docs/ui.md "Notifications"): what this browser can
// do, and the bookkeeping that ties its PushSubscription to the daemon's
// per-project subscription row. The daemon side is internal/daemon/push.go.

export type PushEventKind =
  | "initiative.needs_judgment"
  | "change_set.ready_for_review"
  | "verify.failed"
  | "outcome.drifted";

export const PUSH_EVENT_LABELS: Record<PushEventKind, string> = {
  "initiative.needs_judgment": "An initiative needs your judgment",
  "change_set.ready_for_review": "A change set is ready for review",
  "verify.failed": "Verification failed",
  "outcome.drifted": "An outcome metric is drifting",
};

export function pushEventLabel(kind: string): string {
  return PUSH_EVENT_LABELS[kind as PushEventKind] ?? kind;
}

export interface PushSubscriptionView {
  id: string;
  label?: string;
  endpoint_host: string;
  events: PushEventKind[];
  created_at: string;
  last_delivered_at?: string;
  last_error?: string;
}

export interface PushEnv {
  secureContext: boolean;
  serviceWorker: boolean;
  pushManager: boolean;
  notification: boolean;
  permission: NotificationPermission | "unsupported";
}

export function readPushEnv(): PushEnv {
  if (typeof window === "undefined") {
    return { secureContext: false, serviceWorker: false, pushManager: false, notification: false, permission: "unsupported" };
  }
  const notification = "Notification" in window;
  return {
    secureContext: window.isSecureContext,
    serviceWorker: "serviceWorker" in navigator,
    pushManager: "PushManager" in window,
    notification,
    permission: notification ? Notification.permission : "unsupported",
  };
}

// pushUnavailableReason says why this browser cannot get notifications, or
// "" when it can. In the order a person can do something about them.
export function pushUnavailableReason(env: PushEnv): string {
  if (!env.secureContext) {
    return "Notifications need a secure page: open idea-harness over HTTPS (for example through `tailscale serve`) or on localhost.";
  }
  if (!env.serviceWorker || !env.pushManager || !env.notification) {
    return "This browser does not support web push. On an iPhone or iPad, add idea-harness to the Home Screen first (Share → Add to Home Screen) and open it from there.";
  }
  if (env.permission === "denied") {
    return "Notifications are blocked for this site. Allow them in the browser's site settings, then come back.";
  }
  return "";
}

// urlBase64ToUint8Array decodes the daemon's VAPID public key (base64url, no
// padding) into the BufferSource PushManager.subscribe wants.
export function urlBase64ToUint8Array(b64: string): Uint8Array {
  const padded = b64 + "=".repeat((4 - (b64.length % 4)) % 4);
  const raw = atob(padded.replace(/-/g, "+").replace(/_/g, "/"));
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

// The daemon's subscription id for this browser, per project. The browser
// has one PushSubscription; each project it is turned on for holds a row of
// its own, so turning one project off never silences another.
export const pushIdKey = (projectId: string) => `idea-harness:push:${projectId}`;

export function storedPushId(projectId: string, storage: Pick<Storage, "getItem"> | undefined = safeStorage()): string {
  try {
    return storage?.getItem(pushIdKey(projectId)) ?? "";
  } catch {
    return "";
  }
}

export function storePushId(projectId: string, id: string, storage: Pick<Storage, "setItem" | "removeItem"> | undefined = safeStorage()): void {
  try {
    if (id) storage?.setItem(pushIdKey(projectId), id);
    else storage?.removeItem(pushIdKey(projectId));
  } catch {
    /* not persisted */
  }
}

function safeStorage(): Storage | undefined {
  try {
    return typeof window !== "undefined" ? window.localStorage : undefined;
  } catch {
    return undefined;
  }
}

// toggleEvent flips one kind in a subscription's list, keeping the daemon's
// canonical order so the PATCH body is stable.
export function toggleEvent(events: string[], kind: PushEventKind, order: string[]): string[] {
  const set = new Set(events);
  if (set.has(kind)) set.delete(kind);
  else set.add(kind);
  return order.filter((k) => set.has(k));
}
