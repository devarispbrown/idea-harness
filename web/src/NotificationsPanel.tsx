import { useCallback, useEffect, useState } from "react";
import { del, get, patch, post } from "./api";
import { deviceName } from "./remote";
import {
  pushEventLabel,
  pushUnavailableReason,
  readPushEnv,
  storePushId,
  storedPushId,
  toggleEvent,
  urlBase64ToUint8Array,
  type PushEventKind,
  type PushSubscriptionView,
} from "./push";
import { forgetRenewal, loadRenewal, saveRenewal } from "./pushRenewalStore";

// NotificationsPanel is Settings → Notifications: Web Push for the project
// being viewed, per browser, with a toggle per event kind.
//
// The browser's permission prompt appears only when the person presses
// "Turn on" -- never on page load, which browsers penalise and people
// rightly dismiss. Delivery is the daemon's (internal/daemon/push.go): each
// subscription is a subscriber on the project's integration bus.
export default function NotificationsPanel({ projectId }: { projectId: string }) {
  const [env] = useState(readPushEnv);
  const [subs, setSubs] = useState<PushSubscriptionView[] | null>(null);
  const [kinds, setKinds] = useState<string[]>([]);
  const [mine, setMine] = useState(() => storedPushId(projectId));
  const [busy, setBusy] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const base = `/projects/${encodeURIComponent(projectId)}/push/subscriptions`;

  const load = useCallback(async () => {
    try {
      const v = await get<{ subscriptions: PushSubscriptionView[]; event_kinds: string[] }>(base);
      setSubs(v.subscriptions ?? []);
      setKinds(v.event_kinds ?? []);
      // The daemon removes a subscription its push service reported gone;
      // forget the id so this browser offers to turn notifications on again.
      const list = v.subscriptions ?? [];
      let stored = storedPushId(projectId);
      // The service worker may have renewed this browser's row onto another
      // id (a renewal folded into an existing row); it records that in the
      // renewal store, which the page then adopts.
      const renewal = await loadRenewal(projectId);
      if (renewal && renewal.subscriptionId !== stored && list.some((s) => s.id === renewal.subscriptionId)) {
        stored = renewal.subscriptionId;
        storePushId(projectId, stored);
      }
      if (stored && !list.some((s) => s.id === stored)) {
        storePushId(projectId, "");
        await forgetRenewal(projectId);
        setMine("");
      } else {
        setMine(stored);
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [base, projectId]);

  useEffect(() => {
    load();
  }, [load]);

  const unavailable = pushUnavailableReason(env);
  const mySub = subs?.find((s) => s.id === mine);

  const turnOn = async () => {
    setBusy("on");
    setError("");
    setMessage("");
    try {
      // Asked here, on the click, and nowhere else.
      const permission = await Notification.requestPermission();
      if (permission !== "granted") {
        setError("Notifications were not allowed. You can change that in the browser's site settings.");
        return;
      }
      const { public_key } = await get<{ public_key: string }>("/push/vapid-public-key");
      const reg = await navigator.serviceWorker.ready;
      const existing = await reg.pushManager.getSubscription();
      const sub =
        existing ??
        (await reg.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: urlBase64ToUint8Array(public_key) as BufferSource,
        }));
      const json = sub.toJSON();
      const saved = await post<PushSubscriptionView>(base, {
        endpoint: json.endpoint,
        keys: json.keys,
        label: deviceName(navigator.userAgent),
      });
      storePushId(projectId, saved.id);
      // For the service worker, should the push service replace this
      // subscription while no page is open (web/public/sw-push.js).
      await saveRenewal({ projectId, subscriptionId: saved.id, renewalToken: saved.renewal_token ?? "", vapidKey: public_key });
      setMine(saved.id);
      setMessage("Notifications are on for this browser.");
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy("");
    }
  };

  const remove = async (id: string) => {
    setBusy("remove-" + id);
    setError("");
    try {
      await del(`${base}/${encodeURIComponent(id)}`);
      // The browser's own PushSubscription stays: other projects may be using
      // it. Only this project's row goes.
      if (id === mine) {
        storePushId(projectId, "");
        await forgetRenewal(projectId);
        setMine("");
      }
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy("");
    }
  };

  const setEvents = async (s: PushSubscriptionView, kind: PushEventKind) => {
    setBusy("events-" + s.id);
    setError("");
    try {
      await patch(`${base}/${encodeURIComponent(s.id)}`, { events: toggleEvent(s.events, kind, kinds) });
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy("");
    }
  };

  const test = async (id: string) => {
    setBusy("test-" + id);
    setError("");
    setMessage("");
    try {
      const r = await post<{ ok: boolean; error?: string; gone?: boolean }>(`${base}/${encodeURIComponent(id)}/test`, {});
      if (r.ok) setMessage("Test notification sent.");
      else setError(r.gone ? "The push service says this browser is no longer subscribed; it was removed." : r.error ?? "The test failed.");
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy("");
    }
  };

  return (
    <section className="card notifications-panel">
      <h2>Notifications</h2>
      <p className="muted">
        Web push for this project when something needs you — on this device, even with the tab closed. Each
        browser chooses its own events.
      </p>
      {unavailable && <p className="warn-text">{unavailable}</p>}
      {!unavailable && !mySub && (
        <button className="cs-green" disabled={busy !== ""} onClick={turnOn}>
          {busy === "on" ? "Turning on…" : "Turn on notifications on this device"}
        </button>
      )}
      {subs && subs.length > 0 && (
        <ul className="push-subs">
          {subs.map((s) => (
            <li key={s.id} className="push-sub">
              <div className="push-sub-head">
                <strong>{s.label || "Browser"}</strong>
                {s.id === mine && <span className="idchip chip-inactive">this device</span>}
                <span className="muted"> via {s.endpoint_host}</span>
              </div>
              <fieldset className="push-events">
                <legend className="sr-only">Events that notify {s.label || "this browser"}</legend>
                {kinds.map((k) => (
                  <label key={k} className="push-event">
                    <input
                      type="checkbox"
                      checked={s.events.includes(k as PushEventKind)}
                      disabled={busy !== ""}
                      onChange={() => setEvents(s, k as PushEventKind)}
                    />
                    {pushEventLabel(k)}
                  </label>
                ))}
              </fieldset>
              {s.last_error && <p className="error">Last delivery failed: {s.last_error}</p>}
              <div className="push-sub-actions">
                <button className="cs-secondary" disabled={busy !== ""} onClick={() => test(s.id)}>
                  {busy === "test-" + s.id ? "Sending…" : "Send a test"}
                </button>
                <button className="cs-secondary" disabled={busy !== ""} onClick={() => remove(s.id)}>
                  {s.id === mine ? "Turn off on this device" : "Remove"}
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
      {subs && subs.length === 0 && unavailable === "" && <p className="muted">No device gets notifications for this project yet.</p>}
      {message && (
        <p className="muted" role="status">
          {message}
        </p>
      )}
      {error && <p className="error">{error}</p>}
    </section>
  );
}
