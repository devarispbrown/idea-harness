// displayTitle picks which title an initiative shows (wave 3 / JTBD 6: "Home
// titles were the whole 600-character prompt"). Initiatives gain a generated
// `short_title` (<= 60 chars); the full idea stays in `title`/`description`.
// The UI shows `short_title || title` everywhere a title appears: home
// cards, nav and headers (see ProductHome.tsx, PhaseNav.tsx).
//
// A pre-wave-3 daemon (or a row written before this field existed) sends no
// short_title at all -- optional chaining at the call site, plus the `||`
// here, means that initiative falls back to its full `title` exactly as
// before rather than showing "undefined" or a blank header.
export function displayTitle(i: { short_title?: string; title: string }): string {
  return i.short_title || i.title;
}
