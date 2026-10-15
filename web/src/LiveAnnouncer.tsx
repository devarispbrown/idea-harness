import { useEffect, useState } from "react";
import { nextAnnouncement, onAnnounce } from "./announce";

// LiveAnnouncer is the app's one polite live region (see announce.ts). It is
// on the page from the first render, empty: a region inserted together with
// its text is not reliably announced.
export default function LiveAnnouncer() {
  const [text, setText] = useState("");
  useEffect(() => onAnnounce((m) => setText((prev) => nextAnnouncement(prev, m))), []);
  return (
    <div className="sr-only" role="status" aria-live="polite" aria-atomic="true" data-testid="live-announcer">
      {text}
    </div>
  );
}
