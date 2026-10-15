import { useCallback, useEffect, useState } from "react";
import { get, post, resource } from "./api";
import { navigate } from "./router";
import NotFound from "./NotFound";

interface Decision {
  id: string;
  code: string;
  question: string;
  recommendation: string;
  options: string[];
  status: string;
}
interface DiscussionMessage {
  id: string;
  author: string;
  message: string;
}
interface Changed {
  groups: { category: string; items: string[] }[];
  through: number;
}

export default function DecisionPage({
  projectId,
  initiativeId,
  decisionId,
}: {
  projectId: string;
  initiativeId: string;
  decisionId: string;
}) {
  const [decision, setDecision] = useState<Decision | null>(null);
  const [thread, setThread] = useState<DiscussionMessage[]>([]);
  const [message, setMessage] = useState("");
  const [changed, setChanged] = useState<Changed | null>(null);
  // Whether the decisions list has loaded at least once, so "no such
  // decision" is only said after the page has actually looked.
  const [loaded, setLoaded] = useState(false);

  const load = useCallback(async () => {
    const [ds, th, ch] = await Promise.all([
      get<Decision[]>(resource(projectId, initiativeId, "decisions")).catch(() => []),
      get<DiscussionMessage[]>(`/projects/${projectId}/decisions/${decisionId}/discussion`).catch(() => []),
      get<Changed>(`/projects/${projectId}/initiatives/${initiativeId}/changed`).catch(() => null),
    ]);
    // By id or by code: a link that says DEC-1 should land on DEC-1.
    setDecision((ds ?? []).find((d) => d.id === decisionId || d.code === decisionId) ?? null);
    setThread(th ?? []);
    setChanged(ch);
    setLoaded(true);
  }, [projectId, initiativeId, decisionId]);

  useEffect(() => {
    load();
  }, [load]);

  const send = async () => {
    if (!message.trim()) return;
    await post(`/projects/${projectId}/decisions/${decisionId}/discuss`, { message: message.trim() });
    setMessage("");
    await load();
  };
  const resolve = async (resolution: string) => {
    await post(`/projects/${projectId}/decisions/${decisionId}/resolve`, { resolution });
    await load();
  };
  const markSeen = async () => {
    if (changed) {
      // initiative_id (M1, review fix) scopes the seen cursor to this
      // initiative -- without it, marking one initiative seen advanced a
      // single project-wide cursor past every other initiative's own
      // still-unseen events too.
      await post(`/projects/${projectId}/seen`, { through: changed.through, initiative_id: initiativeId });
      await load();
    }
  };

  // A bad or stale id used to render an empty discussion with a working Send
  // box, which posted into a decision that does not exist.
  if (loaded && !decision) {
    return (
      <NotFound
        title="Decision not found"
        detail={`There's no decision "${decisionId}" in this initiative.`}
        back={{ label: "← Back to Define", href: `#/i/${initiativeId}/define` }}
      />
    );
  }

  return (
    <div className="phase-page discussion-page">
      <div className="doc-reader-head">
        <button className="ghost" onClick={() => navigate(`#/i/${initiativeId}/define`)}>
          ← Define
        </button>
        {decision && <span className="idchip chip-accent">{decision.code}</span>}
        <span className="phase-title" style={{ fontSize: 13 }}>Discussion</span>
      </div>

      <div className="discussion-main">
        <div className="discussion-col">
          {decision && (
            <section className="discussion-card">
              <div className="jc-title">{decision.question}</div>
              <div className="muted" style={{ marginTop: 4 }}>
                {decision.status === "open"
                  ? "Still open. Discussing does not decide it — you resolve it when you're ready."
                  : "Resolved."}
              </div>
            </section>
          )}
          <div className="discussion-thread">
            {thread.map((m) => (
              <div key={m.id} className={`thread-row ${m.author === "human" ? "human" : "ai"}`}>
                <div className="thread-bubble">
                  <div className="thread-who">{m.author === "human" ? "You" : m.author.replace(/_/g, " ")}</div>
                  <div className="thread-text">{m.message}</div>
                </div>
              </div>
            ))}
          </div>
          <div className="discussion-input">
            <input
              className="text-input"
              aria-label="Message about this decision"
              placeholder="Ask the team something about this decision…"
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && send()}
            />
            <button className="jc-primary" onClick={send}>Send</button>
          </div>
          {decision && decision.status === "open" && (
            <div className="discussion-resolve">
              {decision.recommendation && (
                <button className="jc-primary" onClick={() => resolve(decision.recommendation)}>
                  Resolve: {decision.recommendation}
                </button>
              )}
              {(decision.options ?? []).filter((o) => o !== decision.recommendation).map((o) => (
                <button key={o} className="jc-secondary" onClick={() => resolve(o)}>
                  Resolve: {o}
                </button>
              ))}
            </div>
          )}
        </div>

        <aside className="changed-panel">
          <div className="changed-head">
            <span className="doc-eyebrow" style={{ margin: 0 }}>What changed</span>
            <button className="changed-seen" onClick={markSeen}>Mark seen</button>
          </div>
          <div className="changed-groups">
            {(changed?.groups ?? []).map((g) => (
              <div key={g.category}>
                <div className="changed-group">{g.category}</div>
                <div className="changed-items">
                  {g.items.map((it, i) => (
                    <div key={i} className="changed-item">{it}</div>
                  ))}
                </div>
              </div>
            ))}
            {(!changed || changed.groups.length === 0) && <div className="muted">Nothing new.</div>}
          </div>
        </aside>
      </div>
    </div>
  );
}
