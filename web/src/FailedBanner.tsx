import { useEffect, useRef, useState } from "react";
import { errMessage, isOperationInFlight, shouldResetRetryBusy } from "./buildLogic";
import { get, post } from "./api";

interface OperationRef {
  id: string;
  status: string;
}

// FailedBanner is the failure state for a run: it names the failure and
// offers a retry. Two callers use it today: App.tsx for a whole initiative
// gone to `failed` (retry-with-a-different-model, via `defaultModel`), and
// BuildPage for a single failed `execute_initiative` operation that never
// failed the initiative itself (B2) -- that caller passes `onRetry` instead,
// which re-runs the next task rather than reopening the model picker.
export default function FailedBanner({
  projectId,
  initiativeId,
  reason,
  defaultModel,
  title = "Run failed",
  onRetry,
}: {
  projectId: string;
  initiativeId: string;
  reason?: string;
  defaultModel?: string;
  title?: string;
  onRetry?: () => Promise<string>;
}) {
  const [model, setModel] = useState(defaultModel ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // The operation id our own retry started, so we can tell when IT settles
  // rather than guessing from the poll cadence.
  const [pendingOpId, setPendingOpId] = useState<string | null>(null);
  const prevReasonRef = useRef(reason);

  // F3: retry used to stay on "Retrying..." forever when the retried run
  // failed again, because nothing ever set busy back to false if the
  // initiative's poll never observed an intermediate non-failed status (a
  // fast fail-retry-fail cycle can land between two poll ticks). Resetting
  // explicitly -- on the retried operation settling, or on the failure text
  // changing under us -- doesn't depend on that timing.
  useEffect(() => {
    if (shouldResetRetryBusy(undefined, prevReasonRef.current, reason)) {
      setBusy(false);
      setPendingOpId(null);
    }
    prevReasonRef.current = reason;
  }, [reason]);

  useEffect(() => {
    if (!pendingOpId) return;
    let cancelled = false;
    const poll = async () => {
      let ops: OperationRef[];
      try {
        ops = await get<OperationRef[]>(
          `/projects/${projectId}/initiatives/${initiativeId}/operations`,
        );
      } catch {
        return; // transient error: keep polling, keep busy
      }
      if (cancelled) return;
      const found = (ops ?? []).find((o) => o.id === pendingOpId);
      if (!found || !isOperationInFlight(found.status)) {
        setBusy(false);
        setPendingOpId(null);
      }
    };
    poll();
    const id = setInterval(poll, 1500);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [pendingOpId, projectId, initiativeId]);

  const retry = async () => {
    if (busy) return;
    if (!onRetry && !model.trim()) return;
    setBusy(true);
    setError("");
    try {
      const opId = onRetry
        ? await onRetry()
        : (
            await post<{ operation_id: string }>(
              `/projects/${projectId}/initiatives/${initiativeId}/retry`,
              { model: model.trim() },
            )
          ).operation_id;
      setPendingOpId(opId);
    } catch (e) {
      setError(errMessage(e));
      setBusy(false);
    }
  };

  return (
    <section className="failed-banner" role="alert">
      <span className="failed-dot" aria-hidden="true" />
      <div className="failed-body">
        <div className="failed-title">{title}</div>
        {reason && <div className="failed-reason">{reason}</div>}
      </div>
      {!onRetry && (
        <input
          className="text-input"
          placeholder="provider/model"
          value={model}
          onChange={(e) => setModel(e.target.value)}
          aria-label="Retry model"
        />
      )}
      <button className="cs-secondary" onClick={retry} disabled={busy}>
        {busy ? "Retrying…" : "Retry"}
      </button>
      {error && <div className="failed-error">{error}</div>}
    </section>
  );
}
