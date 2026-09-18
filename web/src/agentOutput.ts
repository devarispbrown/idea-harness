// Pure logic for RunningCard's live "Agent output" panel (L3), extracted so
// the auto-scroll decision is unit-testable without a DOM.

// shouldStickToBottom decides whether newly arrived agent output should pull
// the view down to the latest line, or leave it where the reader put it.
// The naive approach -- always scroll to bottom on new output -- fights
// anyone who has scrolled up to read an earlier line: every 2s poll would
// yank them back down. So "stick" is true only while the visible window is
// already within `threshold` px of the bottom (including when the content
// doesn't overflow the box at all, the common case for a short run) --
// which must be measured BEFORE the new content is appended, since appending
// first and then asking "am I at the bottom" always answers yes once the
// box has clamped scrollTop to its new max.
export function shouldStickToBottom(
  scrollTop: number,
  scrollHeight: number,
  clientHeight: number,
  threshold = 24,
): boolean {
  return scrollHeight - (scrollTop + clientHeight) <= threshold;
}
