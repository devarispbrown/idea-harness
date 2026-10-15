// Remote sign-in (docs/security-model.md "Remote access").
//
// A page served to a browser on the daemon's own machine carries the API
// token and never sees any of this. A page served anywhere else -- a phone
// over `tailscale serve`, a second laptop -- has an empty token, and is
// authenticated by a session cookie it got by exchanging a one-time pairing
// code (`idea ui pair`). The cookie is HttpOnly, so page script never holds
// the session itself; what it holds is the CSRF token every write must echo.

import { setCsrfToken, token } from "./api";

export type AuthState = "token" | "session" | "signed-out" | "unreachable";

export interface UISession {
  id: string;
  label?: string;
  user_agent?: string;
  remote_addr?: string;
  created_at: string;
  last_seen_at: string;
  expires_at: string;
  revoked_at?: string;
}

interface SessionView {
  auth: "token" | "session";
  csrf_token?: string;
  session?: UISession;
}

// hasToken reports whether this page was served with the API token -- that
// is, to a browser on the daemon's machine.
export function hasToken(): boolean {
  return token !== "";
}

// resolveAuth decides how this page is authenticated. With a token there is
// nothing to ask. Without one, GET /ui/session says whether the browser's
// cookie is a live session -- and returns the CSRF token the page needs for
// writes, which is how a reload recovers it.
export async function resolveAuth(fetchFn: typeof fetch = fetch): Promise<AuthState> {
  if (hasToken()) return "token";
  let res: Response;
  try {
    res = await fetchFn("/api/v1/ui/session", { credentials: "same-origin" });
  } catch {
    return "unreachable";
  }
  if (res.status === 401) return "signed-out";
  if (!res.ok) return "unreachable";
  const view = (await res.json()) as SessionView;
  if (view.auth === "session" && view.csrf_token) {
    setCsrfToken(view.csrf_token);
    return "session";
  }
  return view.auth === "token" ? "token" : "signed-out";
}

// normalizeCode tidies what a person typed: the daemon ignores case, spaces
// and dashes anyway, but showing it back as XXXXX-XXXXX is easier to check
// against the terminal.
export function normalizeCode(input: string): string {
  const raw = input.toUpperCase().replace(/[^0-9A-Z]/g, "").slice(0, 10);
  return raw.length > 5 ? raw.slice(0, 5) + "-" + raw.slice(5) : raw;
}

// loginErrorMessage turns the sign-in endpoint's answers into what to do next.
export function loginErrorMessage(status: number, message: string): string {
  switch (status) {
    case 401:
      return "That code didn't work — it may be mistyped, already used, or more than ten minutes old. Run `idea ui pair` again for a new one.";
    case 429:
      // The daemon says which lock it is -- this device only (the code still
      // works later), or everyone (codes cancelled) -- and for how long.
      return message || "Too many wrong codes. Wait up to 15 minutes before trying again; if sign-in is still refused, run `idea ui pair` for a new code.";
    case 403:
      return "This page can't sign in from here: open the idea-harness address directly rather than through another site.";
    default:
      return message || `Sign-in failed (${status}).`;
  }
}

// login exchanges a pairing code for a session. On success the cookie is
// set by the response and the CSRF token is kept for later writes.
export async function login(code: string, label: string, fetchFn: typeof fetch = fetch): Promise<{ ok: true } | { ok: false; error: string }> {
  let res: Response;
  try {
    res = await fetchFn("/api/v1/ui/login", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(label ? { code, label } : { code }),
    });
  } catch {
    return { ok: false, error: "The daemon is not reachable." };
  }
  if (!res.ok) {
    let msg = "";
    try {
      msg = ((await res.json()) as { error?: string }).error ?? "";
    } catch {
      /* not JSON */
    }
    return { ok: false, error: loginErrorMessage(res.status, msg) };
  }
  const view = (await res.json()) as SessionView;
  setCsrfToken(view.csrf_token ?? "");
  return { ok: true };
}

// deviceName guesses a label for this device from the browser, so a session
// list reads "iPhone" rather than a user agent string.
export function deviceName(ua: string): string {
  const l = ua.toLowerCase();
  if (l.includes("iphone")) return "iPhone";
  if (l.includes("ipad")) return "iPad";
  if (l.includes("android")) return "Android";
  if (l.includes("mac os")) return "Mac";
  if (l.includes("windows")) return "Windows";
  if (l.includes("linux")) return "Linux";
  return "Browser";
}
