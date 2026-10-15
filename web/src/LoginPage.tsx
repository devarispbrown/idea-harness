import { useState, type FormEvent } from "react";
import { deviceName, login, normalizeCode } from "./remote";

// LoginPage is what a browser that is not on the daemon's machine sees until
// it signs in (docs/security-model.md "Remote access"). It asks for a pairing
// code -- printed by `idea ui pair` on the daemon's machine -- and never for
// the API token, which a remote browser is not given.
export default function LoginPage({ onSignedIn, expired = false }: { onSignedIn: () => void; expired?: boolean }) {
  const [code, setCode] = useState("");
  const [label, setLabel] = useState(() => (typeof navigator !== "undefined" ? deviceName(navigator.userAgent) : ""));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setError("");
    const res = await login(code, label.trim());
    setBusy(false);
    if (res.ok) {
      onSignedIn();
      return;
    }
    setError(res.error);
  };

  const complete = code.replace(/-/g, "").length === 10;

  return (
    <main className="empty login-page">
      <h1>Sign in to idea-harness</h1>
      {expired && (
        <p className="warn-text" role="status">
          This browser was signed out — the session expired or was revoked.
        </p>
      )}
      <p className="muted">
        On the computer running the daemon, run <code>idea ui pair</code> and type the code it prints. A
        code works once, for ten minutes.
      </p>
      <form className="login-form" onSubmit={submit}>
        <label className="note-field-label" htmlFor="pairing-code">
          Pairing code
        </label>
        <input
          id="pairing-code"
          className="text-input login-code"
          inputMode="text"
          autoComplete="one-time-code"
          autoCapitalize="characters"
          autoCorrect="off"
          spellCheck={false}
          placeholder="XXXXX-XXXXX"
          value={code}
          onChange={(e) => setCode(normalizeCode(e.target.value))}
          autoFocus
          aria-describedby={error ? "login-error" : undefined}
        />
        <label className="note-field-label" htmlFor="device-label">
          Name this device <span className="muted">(shown in the session list)</span>
        </label>
        <input
          id="device-label"
          className="text-input"
          value={label}
          maxLength={80}
          onChange={(e) => setLabel(e.target.value)}
        />
        <button type="submit" className="cs-green login-submit" disabled={busy || !complete}>
          {busy ? "Signing in…" : "Sign in"}
        </button>
        {error && (
          <p id="login-error" className="error" role="alert">
            {error}
          </p>
        )}
      </form>
    </main>
  );
}
