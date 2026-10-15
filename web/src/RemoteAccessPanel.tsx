import { useCallback, useEffect, useState } from "react";
import { del, get, post, UNAUTHORIZED_EVENT } from "./api";
import { deviceName, hasToken, type UISession } from "./remote";

// RemoteAccessPanel is Settings → Remote access: the browsers signed in from
// other devices (docs/security-model.md "Remote access"), with Revoke on each,
// Sign out for this one, and Sign out everywhere. Signing a device IN is not
// here on purpose: a pairing code is minted with the API token, on the
// daemon's machine (`idea ui pair`), never from a browser session.
export default function RemoteAccessPanel() {
  const [sessions, setSessions] = useState<UISession[] | null>(null);
  const [current, setCurrent] = useState("");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const v = await get<{ sessions: UISession[]; current?: string }>("/ui/sessions");
      setSessions(v.sessions ?? []);
      setCurrent(v.current ?? "");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const signedOut = () => window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));

  const revoke = async (id: string) => {
    setBusy(id);
    setError("");
    try {
      await del(`/ui/sessions/${encodeURIComponent(id)}`);
      if (id === current) return signedOut();
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy("");
    }
  };

  const signOut = async () => {
    setBusy("logout");
    try {
      await post("/ui/logout", {});
    } finally {
      setBusy("");
      signedOut();
    }
  };

  const signOutEverywhere = async () => {
    if (!window.confirm("Sign out every browser on another device? Each will need a new pairing code.")) return;
    setBusy("all");
    setError("");
    try {
      await del("/ui/sessions");
      if (current) return signedOut();
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy("");
    }
  };

  const live = (sessions ?? []).filter((s) => !s.revoked_at && new Date(s.expires_at).getTime() > Date.now());

  return (
    <section className="card remote-access-panel">
      <h2>Remote access</h2>
      <p className="muted">
        Use idea-harness from your phone or another computer. On the daemon's machine, run{" "}
        <code>idea ui pair</code> for a one-time code, then open this UI on the other device and enter it.
        Setup (Tailscale, TLS): docs/configuration.md, "Remote access".
      </p>
      {sessions !== null && live.length === 0 && <p className="muted">No other device is signed in.</p>}
      {live.length > 0 && (
        <ul className="remote-sessions">
          {live.map((s) => (
            <li key={s.id} className="remote-session">
              <div>
                <strong>{s.label || deviceName(s.user_agent ?? "")}</strong>
                {s.id === current && <span className="idchip chip-inactive">this device</span>}
                <div className="muted remote-session-meta">
                  {s.remote_addr ? `from ${s.remote_addr} · ` : ""}last seen {new Date(s.last_seen_at).toLocaleString()} · until{" "}
                  {new Date(s.expires_at).toLocaleDateString()}
                </div>
              </div>
              <button className="cs-secondary" disabled={busy !== ""} onClick={() => revoke(s.id)}>
                {busy === s.id ? "Revoking…" : s.id === current ? "Sign out" : "Revoke"}
              </button>
            </li>
          ))}
        </ul>
      )}
      <div className="remote-access-actions">
        {!hasToken() && (
          <button className="cs-secondary" disabled={busy !== ""} onClick={signOut}>
            {busy === "logout" ? "Signing out…" : "Sign out of this device"}
          </button>
        )}
        {live.length > 0 && (
          <button className="cs-danger" disabled={busy !== ""} onClick={signOutEverywhere}>
            {busy === "all" ? "Signing out…" : "Sign out everywhere"}
          </button>
        )}
      </div>
      {error && <p className="error">{error}</p>}
    </section>
  );
}
