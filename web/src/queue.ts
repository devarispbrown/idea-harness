// Queue ranking display (D-b): "Nothing showed 'what needs me' across
// initiatives." Pure sort/format logic for the cross-initiative "Needs you"
// view, so it is unit-testable without a DOM.
//
// Wire contract (wave 3, GET /projects/{projectId}/queue):
//   [{initiative_id, short_title, kind, label, rank, href}], highest impact
//   first. The backend already orders the array; `sortByRank` is a defensive
//   re-sort (ascending -- rank 1 is the most urgent) so a client rendering
//   this list is correct even if it receives the array out of order, or a
//   subset re-fetched piecemeal, rather than trusting fetch order blindly.
export interface QueueItem {
  initiative_id: string;
  short_title: string;
  kind: string;
  label: string;
  rank: number;
  href: string;
}

// sortByRank orders ascending by rank (1 = most urgent), stable for equal
// ranks so items that tie keep the order the server sent them in.
export function sortByRank(items: QueueItem[]): QueueItem[] {
  return items
    .map((item, index) => ({ item, index }))
    .sort((a, b) => a.item.rank - b.item.rank || a.index - b.index)
    .map(({ item }) => item);
}

// queueItemText is the one-line display for a queue row: the initiative's
// short title, then what needs attention there.
export function queueItemText(item: QueueItem): string {
  return `${item.short_title} — ${item.label}`;
}

// queueKindBadge is a short badge word for the item's kind (decision,
// question, finding, ...), title-cased for display. An unrecognized/future
// kind still renders (title-cased) rather than vanishing (same "nothing may
// be silently unshown" rule as judgmentScope.ts's otherOpenFindings).
export function queueKindBadge(kind: string): string {
  if (!kind) return "Item";
  return kind.charAt(0).toUpperCase() + kind.slice(1);
}
