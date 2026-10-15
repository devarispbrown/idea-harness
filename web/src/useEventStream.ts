import { useEffect, useRef, useState } from "react";
import { token } from "./api";
import { backoffDelay, shouldFallBack, streamURL } from "./runEvents";

// openEventStream follows one of the daemon's SSE streams and keeps following
// it through everything a long-lived tab meets:
//
//   - resume: the caller owns the cursor (getCursor). EventSource sends
//     Last-Event-ID itself on its own reconnects; when this code recreates the
//     stream (after a backoff, or the tab coming back into view) it passes the
//     cursor as ?after=, so only what was missed is sent -- never the whole
//     history again.
//   - backoff: an error closes the stream and reconnects after 1s, 2s, 4s ...
//     30s (jittered), reset by a successful open.
//   - visibility: a hidden tab closes its stream (a background tab holding a
//     connection per stream spends the browser's per-host connection budget)
//     and reopens it, resumed, when it is shown again.
//   - fallback: no EventSource, or three failures in a row before it ever
//     opened (a proxy that will not pass text/event-stream), and it polls
//     instead -- poll() every pollMs while visible.
//   - end: a named `end` message (a run stream, once the run is over) closes
//     the stream for good.

export type StreamMode = "connecting" | "live" | "polling" | "ended" | "idle";

export interface StreamSpec {
  // path is the stream's path under /api/v1, e.g. /projects/p/events.
  path: string;
  // eventNames are the SSE events delivered ("message" = unnamed events).
  eventNames: string[];
  getCursor: () => string;
  onMessage: (data: string, id: string, eventName: string) => void;
  onEnd?: (data: string) => void;
  onMode?: (mode: StreamMode) => void;
  poll?: () => Promise<void> | void;
  pollMs?: number;
}

export function openEventStream(spec: StreamSpec): () => void {
  let es: EventSource | null = null;
  let retry: ReturnType<typeof setTimeout> | null = null;
  let pollTimer: ReturnType<typeof setInterval> | null = null;
  let attempt = 0;
  let failuresBeforeOpen = 0;
  let opened = false;
  let polling = false;
  let ended = false;
  let disposed = false;
  const hasES = typeof EventSource !== "undefined";
  const setMode = (m: StreamMode) => spec.onMode?.(m);

  const closeStream = () => {
    if (es) {
      es.close();
      es = null;
    }
    if (retry) {
      clearTimeout(retry);
      retry = null;
    }
  };

  const runPoll = async () => {
    if (typeof document !== "undefined" && document.hidden) return;
    try {
      await spec.poll?.();
    } catch {
      /* the next tick tries again */
    }
  };

  const startPolling = () => {
    closeStream();
    polling = true;
    setMode("polling");
    if (!spec.poll) return;
    void runPoll();
    if (!pollTimer) pollTimer = setInterval(runPoll, spec.pollMs ?? 2500);
  };

  const connect = () => {
    if (disposed || ended || polling) return;
    if (typeof document !== "undefined" && document.hidden) return;
    if (shouldFallBack(hasES, failuresBeforeOpen)) {
      startPolling();
      return;
    }
    closeStream();
    setMode("connecting");
    opened = false;
    const src = new EventSource(streamURL(spec.path, spec.getCursor(), token));
    es = src;
    src.onopen = () => {
      opened = true;
      attempt = 0;
      failuresBeforeOpen = 0;
      setMode("live");
    };
    for (const name of spec.eventNames) {
      src.addEventListener(name, (e) => {
        const me = e as MessageEvent<string>;
        spec.onMessage(me.data, me.lastEventId, name);
      });
    }
    src.addEventListener("end", (e) => {
      ended = true;
      closeStream();
      setMode("ended");
      spec.onEnd?.((e as MessageEvent<string>).data);
    });
    src.onerror = () => {
      if (es !== src) return;
      if (!opened) failuresBeforeOpen++;
      closeStream();
      setMode("connecting");
      retry = setTimeout(connect, backoffDelay(attempt++));
    };
  };

  const onVisibility = () => {
    if (document.hidden) {
      closeStream();
    } else if (polling) {
      void runPoll();
    } else {
      attempt = 0;
      connect();
    }
  };

  connect();
  if (typeof document !== "undefined") document.addEventListener("visibilitychange", onVisibility);
  return () => {
    disposed = true;
    closeStream();
    if (pollTimer) clearInterval(pollTimer);
    if (typeof document !== "undefined") document.removeEventListener("visibilitychange", onVisibility);
  };
}

export interface EventStreamOptions extends Omit<StreamSpec, "onMode" | "eventNames"> {
  // key identifies the stream; a change closes the old one and opens the
  // new. "" means no stream.
  key: string;
  eventName?: string;
}

// useEventStream is openEventStream as a hook. Every callback is read through
// a ref, so a caller passing fresh closures each render does not reconnect;
// `key` is what restarts the stream.
export function useEventStream(opts: EventStreamOptions): StreamMode {
  const ref = useRef(opts);
  ref.current = opts;
  const [mode, setMode] = useState<StreamMode>("idle");
  useEffect(() => {
    if (!opts.key) {
      setMode("idle");
      return;
    }
    return openEventStream({
      path: ref.current.path,
      eventNames: [ref.current.eventName ?? "message"],
      getCursor: () => ref.current.getCursor(),
      onMessage: (d, id, n) => ref.current.onMessage(d, id, n),
      onEnd: (d) => ref.current.onEnd?.(d),
      onMode: setMode,
      poll: () => ref.current.poll?.(),
      pollMs: ref.current.pollMs,
    });
    // path is part of the key by construction.
  }, [opts.key]);
  return mode;
}

// ---------- the shared project stream ----------

// One project stream per project per tab, shared by every component that
// wants to know "something changed" (App's badges, the running card): each
// would otherwise hold its own connection.
interface ProjectStream {
  listeners: Set<() => void>;
  modeListeners: Set<(live: boolean) => void>;
  live: boolean;
  cursor: string;
  close: () => void;
}

const projectStreams = new Map<string, ProjectStream>();

function subscribeProject(projectId: string, onChange: () => void, onLive: (live: boolean) => void): () => void {
  let ps = projectStreams.get(projectId);
  if (!ps) {
    // "now": the first connection skips the product-graph backlog (every
    // subscriber fetched its own data on mount); reconnects resume from the
    // last id seen, so nothing in between is missed.
    const created: ProjectStream = { listeners: new Set(), modeListeners: new Set(), live: false, cursor: "now", close: () => {} };
    ps = created;
    projectStreams.set(projectId, created);
    created.close = openEventStream({
      path: `/projects/${encodeURIComponent(projectId)}/events`,
      eventNames: ["message"],
      getCursor: () => created.cursor,
      onMessage: (_data, id) => {
        if (id) created.cursor = id;
        created.listeners.forEach((l) => l());
      },
      onMode: (m) => {
        const live = m === "live";
        if (live !== created.live) {
          created.live = live;
          created.modeListeners.forEach((l) => l(live));
        }
      },
    });
  }
  const s = ps;
  s.listeners.add(onChange);
  s.modeListeners.add(onLive);
  onLive(s.live);
  return () => {
    s.listeners.delete(onChange);
    s.modeListeners.delete(onLive);
    if (s.listeners.size === 0) {
      s.close();
      projectStreams.delete(projectId);
    }
  };
}

// useProjectChanges calls onChange (debounced) whenever the project's event
// stream carries a product-graph event, so a page can refetch on change
// instead of on a timer. It returns whether the stream is live: callers keep
// their own timer as a slow backstop while it is, and at their usual pace
// while it is not (polling is the stream's fallback).
export function useProjectChanges(projectId: string, onChange: () => void, debounceMs = 300): boolean {
  const cb = useRef(onChange);
  cb.current = onChange;
  const [live, setLive] = useState(false);
  useEffect(() => {
    if (!projectId) {
      setLive(false);
      return;
    }
    let timer: ReturnType<typeof setTimeout> | null = null;
    // The first connect replays the product-graph log (the stream's
    // contract with no cursor); the debounce makes that burst one refetch.
    const unsubscribe = subscribeProject(
      projectId,
      () => {
        if (timer) clearTimeout(timer);
        timer = setTimeout(() => cb.current(), debounceMs);
      },
      setLive,
    );
    return () => {
      if (timer) clearTimeout(timer);
      unsubscribe();
    };
  }, [projectId, debounceMs]);
  return live;
}
