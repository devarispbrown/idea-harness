// Push subscription renewal, for the service worker (sw.js importScripts this).
//
// A push service may replace a browser's subscription whenever it likes --
// key rotation, expiry, a browser reset -- and says so only to the service
// worker, as a `pushsubscriptionchange` event that may fire with no page
// open. Without handling it, notifications stop silently: the daemon keeps
// pushing to an endpoint that no longer exists until the push service says
// 410 and the daemon deletes the row.
//
// The worker cannot authenticate the way a page does. It never sees the API
// token (on loopback only the page holds it), and a session cookie may have
// lapsed or never existed; a CSRF token is derived from that session, so it
// is no help either. Instead, subscribing returns a per-subscription renewal
// token (internal/daemon/push.go, handleRenewPushSubscription). The page keeps
// it here, in IndexedDB -- the one store a worker and a page share -- and the
// worker spends it on POST .../push/subscriptions/{id}/renew, which re-points
// that one row at the new endpoint and hands back the next token. The token
// can do nothing else; docs/security-model.md ("Remote access") says what a
// stolen one buys.
//
// A classic script, not a module: module service workers are not supported
// everywhere this app is installed. It defines self.IdeaPushRenewal and
// nothing else; web/src/swPush.test.ts runs it with fakes.

(function (root) {
  "use strict";

  // Mirrored in web/src/pushRenewalStore.ts; a test checks they agree.
  var DB_NAME = "idea-harness-push";
  var DB_VERSION = 1;
  var STORE = "renewals";

  function reqToPromise(req) {
    return new Promise(function (resolve, reject) {
      req.onsuccess = function () {
        resolve(req.result);
      };
      req.onerror = function () {
        reject(req.error);
      };
    });
  }

  // idbStore is the renewal records in IndexedDB, one per project:
  // { projectId, subscriptionId, renewalToken, vapidKey }.
  function idbStore(idb) {
    function open() {
      var req = idb.open(DB_NAME, DB_VERSION);
      req.onupgradeneeded = function () {
        if (!req.result.objectStoreNames.contains(STORE)) req.result.createObjectStore(STORE, { keyPath: "projectId" });
      };
      return reqToPromise(req);
    }
    function run(mode, fn) {
      return open().then(function (db) {
        var tx = db.transaction(STORE, mode);
        var out = reqToPromise(fn(tx.objectStore(STORE)));
        return out.finally(function () {
          db.close();
        });
      });
    }
    return {
      all: function () {
        return run("readonly", function (s) {
          return s.getAll();
        });
      },
      put: function (rec) {
        return run("readwrite", function (s) {
          return s.put(rec);
        });
      },
      remove: function (projectId) {
        return run("readwrite", function (s) {
          return s.delete(projectId);
        });
      },
    };
  }

  function urlBase64ToUint8Array(b64) {
    var padded = b64 + "===".slice((b64.length + 3) % 4);
    var raw = atob(padded.replace(/-/g, "+").replace(/_/g, "/"));
    var out = new Uint8Array(raw.length);
    for (var i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
    return out;
  }

  // newSubscriptionFor finds the browser's new subscription: the event's own
  // when the browser supplies it, else a fresh subscribe with the same VAPID
  // key -- the old subscription's, or the one the page stored.
  function newSubscriptionFor(event, pushManager, records) {
    if (event && event.newSubscription) return Promise.resolve(event.newSubscription);
    var key = event && event.oldSubscription && event.oldSubscription.options && event.oldSubscription.options.applicationServerKey;
    if (!key) {
      for (var i = 0; i < records.length; i++) {
        if (records[i].vapidKey) {
          key = urlBase64ToUint8Array(records[i].vapidKey);
          break;
        }
      }
    }
    if (!key) return Promise.resolve(null);
    return pushManager.getSubscription().then(function (existing) {
      // Another tab may already have re-subscribed.
      return existing || pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
    });
  }

  // renewAll re-points every project's row at the new subscription. Returns
  // what happened per project, for the tests and nobody else.
  //
  //   - 200: the row moved; keep the rotated token (and the row id, which
  //     changes when the renewal folded into an existing row).
  //   - 401: the daemon will not renew it (deleted, token spent elsewhere);
  //     forget it, so Settings offers "Turn on" again.
  //   - anything else (offline, 5xx): keep the record. The old endpoint is
  //     dead either way; the next change event, or turning notifications on
  //     again, gets another chance.
  function renewAll(event, deps) {
    return deps.store.all().then(function (records) {
      if (!records || records.length === 0) return [];
      return newSubscriptionFor(event, deps.pushManager, records).then(function (sub) {
        if (!sub) {
          return records.map(function (r) {
            return { projectId: r.projectId, result: "no-subscription" };
          });
        }
        var json = sub.toJSON();
        return records.reduce(function (chain, rec) {
          return chain.then(function (results) {
            var url =
              "/api/v1/projects/" +
              encodeURIComponent(rec.projectId) +
              "/push/subscriptions/" +
              encodeURIComponent(rec.subscriptionId) +
              "/renew";
            return deps
              .fetch(url, {
                method: "POST",
                // The token is the credential; a cookie adds nothing and is
                // not what authorises this.
                credentials: "omit",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ renewal_token: rec.renewalToken, endpoint: json.endpoint, keys: json.keys }),
              })
              .then(function (res) {
                if (res.ok) {
                  return res.json().then(function (view) {
                    var next = {
                      projectId: rec.projectId,
                      subscriptionId: view.id,
                      renewalToken: view.renewal_token,
                      vapidKey: rec.vapidKey,
                    };
                    return deps.store.put(next).then(function () {
                      return results.concat({ projectId: rec.projectId, result: "renewed" });
                    });
                  });
                }
                if (res.status === 401) {
                  return deps.store.remove(rec.projectId).then(function () {
                    return results.concat({ projectId: rec.projectId, result: "forgotten" });
                  });
                }
                return results.concat({ projectId: rec.projectId, result: "kept", status: res.status });
              })
              .catch(function () {
                return results.concat({ projectId: rec.projectId, result: "kept" });
              });
          });
        }, Promise.resolve([]));
      });
    });
  }

  root.IdeaPushRenewal = {
    DB_NAME: DB_NAME,
    DB_VERSION: DB_VERSION,
    STORE: STORE,
    idbStore: idbStore,
    renewAll: renewAll,
  };
})(self);
