import { useState } from "react";
import { put } from "../api";
import {
  hasErrors,
  linearConfigFrom,
  linearFormFrom,
  slackConfigFrom,
  slackFormFrom,
  validateLinear,
  validateSlack,
  type FieldErrors,
  type LinearConfig,
  type LinearForm,
  type SlackConfig,
  type SlackForm,
} from "./integrationForm";

// IntegrationEditor edits one integration's harness.yaml section through
// PUT /projects/{projectId}/integrations/{name}. Tokens are never typed
// here: the form names the environment variables that hold them, and the
// row above already says whether each is set.
export default function IntegrationEditor({
  projectId,
  name,
  config,
  tokens,
  eventKinds,
  defaultEvents,
  onSaved,
}: {
  projectId: string;
  name: "slack" | "linear";
  config: SlackConfig | LinearConfig | null;
  tokens: Record<string, boolean>;
  eventKinds: string[];
  defaultEvents: string[];
  onSaved: (message: string) => void;
}) {
  const [slack, setSlack] = useState<SlackForm>(() => slackFormFrom(name === "slack" ? (config as SlackConfig | null) : null, defaultEvents));
  const [linear, setLinear] = useState<LinearForm>(() => linearFormFrom(name === "linear" ? (config as LinearConfig | null) : null));
  const [errors, setErrors] = useState<FieldErrors>({});
  const [serverError, setServerError] = useState("");
  const [saving, setSaving] = useState(false);
  const id = (field: string) => `integ-${name}-${field}`;

  const send = async (body: { config: SlackConfig | LinearConfig | null }, done: string) => {
    setSaving(true);
    setServerError("");
    try {
      await put(`/projects/${encodeURIComponent(projectId)}/integrations/${name}`, body);
      onSaved(done);
    } catch (e) {
      setServerError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  const save = () => {
    const e = name === "slack" ? validateSlack(slack, eventKinds) : validateLinear(linear);
    setErrors(e);
    if (hasErrors(e)) return;
    send(
      { config: name === "slack" ? slackConfigFrom(slack, defaultEvents) : linearConfigFrom(linear) },
      `${name === "slack" ? "Slack" : "Linear"} settings saved to harness.yaml.`,
    );
  };

  const remove = () => send({ config: null }, `${name === "slack" ? "Slack" : "Linear"} turned off (its section was removed from harness.yaml).`);

  const err = (field: string) =>
    errors[field] ? (
      <p className="error" id={id(field) + "-error"}>
        {errors[field]}
      </p>
    ) : null;
  const described = (field: string) => (errors[field] ? { "aria-invalid": true, "aria-describedby": id(field) + "-error" } : {});

  const tokenNote = (
    <p className="muted">
      Tokens are read from the environment or the project's <code>.env</code>, never from this form:{" "}
      {Object.entries(tokens).map(([env, set], i) => (
        <span key={env}>
          {i > 0 && ", "}
          <code>{env}</code> {set ? "is set" : "is not set"}
        </span>
      ))}
      .
    </p>
  );

  return (
    <div className="integ-editor">
      {name === "slack" ? (
        <>
          <label className="field-label" htmlFor={id("channel")}>
            Channel ID
          </label>
          <input id={id("channel")} className="text-input" placeholder="C0123ABCD" value={slack.channel} onChange={(e) => setSlack({ ...slack, channel: e.target.value })} {...described("channel")} />
          {err("channel")}
          <fieldset className="integ-events">
            <legend className="field-label">Events posted</legend>
            {eventKinds.map((k) => (
              <label key={k} className="w3-toggle-row">
                <input
                  type="checkbox"
                  checked={slack.events.includes(k)}
                  onChange={(e) =>
                    setSlack({ ...slack, events: e.target.checked ? [...slack.events, k] : slack.events.filter((x) => x !== k) })
                  }
                />
                <code>{k}</code>
              </label>
            ))}
            <button type="button" className="ghost" onClick={() => setSlack({ ...slack, events: [...defaultEvents] })}>
              Reset to the defaults
            </button>
          </fieldset>
          {err("events")}
          <label className="field-label" htmlFor={id("mention")}>
            Mention when a human is needed (user IDs, or here / channel)
          </label>
          <input id={id("mention")} className="text-input" placeholder="here, U0123ABCD" value={slack.mentionOnJudgment} onChange={(e) => setSlack({ ...slack, mentionOnJudgment: e.target.value })} {...described("mentionOnJudgment")} />
          {err("mentionOnJudgment")}
          <label className="field-label" htmlFor={id("allowed")}>
            Who may act from Slack (user IDs; empty means notify-only)
          </label>
          <input id={id("allowed")} className="text-input" placeholder="U0123ABCD, U0456EFGH" value={slack.allowedUsers} onChange={(e) => setSlack({ ...slack, allowedUsers: e.target.value })} {...described("allowedUsers")} />
          {err("allowedUsers")}
          <label className="w3-toggle-row">
            <input type="checkbox" checked={slack.socketMode} onChange={(e) => setSlack({ ...slack, socketMode: e.target.checked })} />
            Buttons and /idea over Socket Mode (needs the app-level token)
          </label>
          <details>
            <summary>Token variable names and deep links</summary>
            <label className="field-label" htmlFor={id("bot-env")}>
              Bot token variable
            </label>
            <input id={id("bot-env")} className="text-input" placeholder="SLACK_BOT_TOKEN" value={slack.botTokenEnv} onChange={(e) => setSlack({ ...slack, botTokenEnv: e.target.value })} {...described("botTokenEnv")} />
            {err("botTokenEnv")}
            <label className="field-label" htmlFor={id("app-env")}>
              App-level token variable
            </label>
            <input id={id("app-env")} className="text-input" placeholder="SLACK_APP_TOKEN" value={slack.appTokenEnv} onChange={(e) => setSlack({ ...slack, appTokenEnv: e.target.value })} {...described("appTokenEnv")} />
            {err("appTokenEnv")}
            <label className="field-label" htmlFor={id("ui-base")}>
              Deep-link root
            </label>
            <input id={id("ui-base")} className="text-input" placeholder="the daemon's own address" value={slack.uiBaseURL} onChange={(e) => setSlack({ ...slack, uiBaseURL: e.target.value })} {...described("uiBaseURL")} />
            {err("uiBaseURL")}
          </details>
        </>
      ) : (
        <>
          <label className="field-label" htmlFor={id("team")}>
            Team key
          </label>
          <input id={id("team")} className="text-input" placeholder="ENG" value={linear.teamKey} onChange={(e) => setLinear({ ...linear, teamKey: e.target.value })} {...described("teamKey")} />
          {err("teamKey")}
          <label className="field-label" htmlFor={id("project")}>
            Project (optional)
          </label>
          <input id={id("project")} className="text-input" value={linear.project} onChange={(e) => setLinear({ ...linear, project: e.target.value })} {...described("project")} />
          {err("project")}
          <label className="w3-toggle-row">
            <input type="checkbox" checked={linear.inbound} onChange={(e) => setLinear({ ...linear, inbound: e.target.checked })} />
            Import labeled issues as initiatives (inbound polling)
          </label>
          <label className="field-label" htmlFor={id("label")}>
            Inbound label
          </label>
          <input id={id("label")} className="text-input" placeholder="idea" value={linear.inboundLabel} onChange={(e) => setLinear({ ...linear, inboundLabel: e.target.value })} />
          <label className="field-label" htmlFor={id("poll")}>
            Poll interval (seconds, at least 15)
          </label>
          <input id={id("poll")} className="text-input" inputMode="numeric" placeholder="60" value={linear.pollIntervalSeconds} onChange={(e) => setLinear({ ...linear, pollIntervalSeconds: e.target.value })} {...described("pollIntervalSeconds")} />
          {err("pollIntervalSeconds")}
          <fieldset className="integ-states">
            <legend className="field-label">State mapping (your Linear workflow's state names)</legend>
            {(
              [
                ["todo", "Todo"],
                ["in_progress", "In Progress"],
                ["in_review", "In Review"],
                ["done", "Done"],
                ["canceled", "Canceled"],
              ] as const
            ).map(([k, def]) => (
              <span key={k} className="integ-state">
                <label htmlFor={id("state-" + k)}>{def}</label>
                <input
                  id={id("state-" + k)}
                  className="text-input"
                  placeholder={def}
                  value={linear.states[k]}
                  onChange={(e) => setLinear({ ...linear, states: { ...linear.states, [k]: e.target.value } })}
                  {...described("state_" + k)}
                />
                {err("state_" + k)}
              </span>
            ))}
          </fieldset>
          <details>
            <summary>API key variable name</summary>
            <label className="field-label" htmlFor={id("key-env")}>
              API key variable
            </label>
            <input id={id("key-env")} className="text-input" placeholder="LINEAR_API_KEY" value={linear.apiKeyEnv} onChange={(e) => setLinear({ ...linear, apiKeyEnv: e.target.value })} {...described("apiKeyEnv")} />
            {err("apiKeyEnv")}
          </details>
        </>
      )}
      {tokenNote}
      <div className="row">
        <button disabled={saving} onClick={save}>
          {config ? `Save ${name === "slack" ? "Slack" : "Linear"} settings` : `Turn on ${name === "slack" ? "Slack" : "Linear"}`}
        </button>
        {config && (
          <button className="ghost" disabled={saving} onClick={remove}>
            Turn off
          </button>
        )}
      </div>
      {serverError && <p className="error">{serverError}</p>}
    </div>
  );
}
