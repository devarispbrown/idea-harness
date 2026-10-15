// The page's half of push subscription renewal (web/public/sw-push.js has
// the worker's half and the why). When this browser subscribes in a project,
// the daemon returns a renewal token; it is kept here, in IndexedDB, because
// that is the one store the service worker can read when the push service
// replaces the subscription with no page open.
//
// Best effort throughout: a browser without IndexedDB (or a private mode
// that refuses it) still gets notifications, it just cannot renew them
// unattended -- the next "Turn on" re-subscribes.

// Mirrored from web/public/sw-push.js; swPush.test.ts checks they agree.
export const RENEWAL_DB = "idea-harness-push";
export const RENEWAL_DB_VERSION = 1;
export const RENEWAL_STORE = "renewals";

export interface RenewalRecord {
  projectId: string;
  subscriptionId: string;
  renewalToken: string;
  // The daemon's VAPID public key (base64url): what the worker subscribes
  // with when the browser hands it no new subscription of its own.
  vapidKey: string;
}

function idb(): IDBFactory | undefined {
  try {
    return typeof indexedDB !== "undefined" ? indexedDB : undefined;
  } catch {
    return undefined;
  }
}

function request<T>(req: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

async function withStore<T>(mode: IDBTransactionMode, fn: (s: IDBObjectStore) => IDBRequest<T>, factory = idb()): Promise<T | undefined> {
  if (!factory) return undefined;
  const open = factory.open(RENEWAL_DB, RENEWAL_DB_VERSION);
  open.onupgradeneeded = () => {
    if (!open.result.objectStoreNames.contains(RENEWAL_STORE)) open.result.createObjectStore(RENEWAL_STORE, { keyPath: "projectId" });
  };
  const db = await request(open);
  try {
    return await request(fn(db.transaction(RENEWAL_STORE, mode).objectStore(RENEWAL_STORE)));
  } finally {
    db.close();
  }
}

export async function saveRenewal(rec: RenewalRecord, factory?: IDBFactory): Promise<void> {
  if (!rec.renewalToken) return;
  try {
    await withStore("readwrite", (s) => s.put(rec), factory ?? idb());
  } catch {
    /* not persisted: renewal will need a page visit */
  }
}

export async function loadRenewal(projectId: string, factory?: IDBFactory): Promise<RenewalRecord | undefined> {
  try {
    return (await withStore("readonly", (s) => s.get(projectId) as IDBRequest<RenewalRecord | undefined>, factory ?? idb())) ?? undefined;
  } catch {
    return undefined;
  }
}

export async function forgetRenewal(projectId: string, factory?: IDBFactory): Promise<void> {
  try {
    await withStore("readwrite", (s) => s.delete(projectId), factory ?? idb());
  } catch {
    /* nothing to forget */
  }
}
