import { navigate } from "./router";

// NotFound is what every id-bearing route renders when its id resolves to
// nothing: a mistyped hash, a stale link, an initiative from another project,
// a decision that was never raised. Before it, only phase routes checked their
// initiative; a bad decision id rendered an empty discussion with a live Send
// box that posted into a decision that does not exist.
//
// `back` defaults to the initiative list; a caller inside an initiative passes
// somewhere closer, e.g. that initiative's Define page.
export default function NotFound({
  title,
  detail,
  back,
}: {
  title: string;
  detail: string;
  back?: { label: string; href: string };
}) {
  const target = back ?? { label: "Back to initiatives", href: "#/" };
  return (
    <div className="empty not-found" role="region" aria-label={title}>
      <h1>{title}</h1>
      <p className="muted">{detail}</p>
      <button className="jc-primary" onClick={() => navigate(target.href)}>
        {target.label}
      </button>
    </div>
  );
}
