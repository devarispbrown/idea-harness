// isHttpUrl (H5, review fix): guards every `href` built from data the daemon
// or a third-party service relayed rather than the UI itself constructed --
// `pr_url` (the last non-empty line of `gh pr create`'s own output) and a PR
// check's `link` (from the CI provider). React does not block a
// `javascript:` (or other script-executing) scheme reaching a rendered
// `<a href>`, so anything short of a real http(s) URL must render as plain,
// unclickable text instead.
export function isHttpUrl(value: string | undefined | null): boolean {
  if (!value) return false;
  return /^https?:\/\//i.test(value.trim());
}
