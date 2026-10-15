import { useRef, type ReactNode, type RefObject } from "react";
import { useDialog } from "./focus";

// InlinePanel is a disclosure that opens in the page under the button that
// asked for it -- Request changes, Ask for a revision, the revert
// confirmation, an objective's edit form. Mounted means open: focus moves
// into it (its first control, or `initial`), Escape inside it calls onClose,
// and when it unmounts focus returns to the button that opened it, instead of
// falling to the top of the page with the panel that held it.
export default function InlinePanel({
  onClose,
  className,
  label,
  labelledBy,
  role,
  initial,
  children,
}: {
  onClose: () => void;
  className?: string;
  label?: string;
  labelledBy?: string;
  // role "alertdialog" for a confirmation that interrupts (revert); a plain
  // form needs none beyond its label.
  role?: string;
  initial?: RefObject<HTMLElement>;
  children: ReactNode;
}) {
  const ref = useRef<HTMLElement>(null);
  useDialog(ref, true, onClose, initial, { modal: false });
  return (
    <section ref={ref} className={className} aria-label={label} aria-labelledby={labelledBy} role={role}>
      {children}
    </section>
  );
}
