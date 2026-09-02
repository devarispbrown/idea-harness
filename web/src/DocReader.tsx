import { useEffect, useState } from "react";
import { get } from "./api";
import { navigate } from "./router";

export default function DocReader({
  projectId,
  initiativeId,
  artifactId,
}: {
  projectId: string;
  initiativeId: string;
  artifactId: string;
}) {
  const [body, setBody] = useState("");
  const [kind, setKind] = useState("");

  useEffect(() => {
    get<{ kind: string; body: string }>(`/projects/${projectId}/artifacts/${artifactId}/body`)
      .then((v) => {
        setBody(v?.body ?? "");
        setKind(v?.kind ?? "");
      })
      .catch(() => setBody(""));
  }, [projectId, artifactId]);

  return (
    <div className="phase-page">
      <div className="doc-reader-head">
        <button className="ghost" onClick={() => navigate(`#/i/${initiativeId}/explore`)}>
          ← Back
        </button>
        {kind && <span className="code">{kind}</span>}
      </div>
      <pre className="doc-reader-body">{body || "No content."}</pre>
    </div>
  );
}
