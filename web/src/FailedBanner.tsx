import { useState } from "react";
import { post } from "./api";

// FailedBanner is the initiative error state: it names the failure and offers a
// retry under a chosen model (retry-with-a-different-model).
export default function FailedBanner({
  projectId,
  initiativeId,
  reason,
  defaultModel,
}: {
  projectId: string;
  initiativeId: string;
  reason?: string;
  defaultModel: string;
}) {
  const [model, setModel] = useState(defaultModel);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const retry = async () => {
    if (!model.trim() || busy) return;
    setBusy(true);
    setError("");
    try {
      await post(`/projects/${projectId}/initiatives/${initiativeId}/retry`, {
        model: model.trim(),
      });
    } catch (e) {
      setError(String(e));
      setBusy(false);
    }
    // On success, leave busy=true until the poll observes the new status.
  };

  return (
    <section className="failed-banner" role="alert">
      <span className="failed-dot" aria-hidden="true" />
      <div className="failed-body">
        <div className="failed-title">Run failed</div>
        {reason && <div className="failed-reason">{reason}</div>}
      </div>
      <input
        className="text-input"
        placeholder="provider/model"
        value={model}
        onChange={(e) => setModel(e.target.value)}
        aria-label="Retry model"
      />
      <button className="cs-secondary" onClick={retry}>
        {busy ? "Retrying…" : "Retry"}
      </button>
      {error && <div className="failed-error">{error}</div>}
    </section>
  );
}
