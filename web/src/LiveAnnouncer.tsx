import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { nextAnnouncement, onAnnounce } from "./announce";

// LiveAnnouncer is the app's one polite live region (see announce.ts). It is
// on the page from the first render, empty: a region inserted together with
// its text is not reliably announced. It lives directly in <body>, exempt
// from the inert a modal puts on the app root (focus.ts, inertOutside), so
// what is announced while a dialog is open is still heard.
export default function LiveAnnouncer() {
  const [text, setText] = useState("");
  useEffect(() => onAnnounce((m) => setText((prev) => nextAnnouncement(prev, m))), []);
  return createPortal(
    <div className="sr-only" role="status" aria-live="polite" aria-atomic="true" data-testid="live-announcer" data-inert-exempt="">
      {text}
    </div>,
    document.body,
  );
}
