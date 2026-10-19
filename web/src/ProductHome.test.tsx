// @vitest-environment jsdom
//
// Component tests for the "New initiative" compose surface that replaced
// ProductHome's single-line idea input: a brief textarea, a file drop-zone
// (click / drag-drop / paste), per-file text/image badges, and an
// always-enabled Start button that hands the brief + File[] to onCreate.
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import ProductHome from "./ProductHome";

afterEach(() => {
  cleanup();
});

// No initiatives means the timeline loader never fetches, so these render
// without a stubbed daemon.
const base = {
  projectId: "p1",
  initiatives: [] as never[],
  attention: [] as never[],
  projectName: "web",
  onOpen: () => {},
  onOpenSettings: () => {},
};

describe("ProductHome compose", () => {
  it("renders a labelled brief textarea and a Start button", () => {
    render(<ProductHome {...base} onCreate={() => {}} />);
    expect(screen.getByLabelText("Brief").tagName).toBe("TEXTAREA");
    expect(screen.getByRole("button", { name: "Start" })).toBeTruthy();
  });

  it("calls onCreate with the brief and no files", () => {
    const onCreate = vi.fn();
    render(<ProductHome {...base} onCreate={onCreate} />);
    fireEvent.change(screen.getByLabelText("Brief"), {
      target: { value: "Add CSV export\n\nfor the reports page" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Start" }));
    expect(onCreate).toHaveBeenCalledWith("Add CSV export\n\nfor the reports page", [], undefined);
  });

  it("no-ops with an empty brief and shows the hint", () => {
    const onCreate = vi.fn();
    render(<ProductHome {...base} onCreate={onCreate} />);
    fireEvent.click(screen.getByRole("button", { name: "Start" }));
    expect(onCreate).not.toHaveBeenCalled();
    expect(screen.getByText(/Write a line or two/)).toBeTruthy();
  });

  it("lists dropped files with text/image badges and passes them to onCreate", () => {
    const onCreate = vi.fn();
    const { container } = render(<ProductHome {...base} onCreate={onCreate} />);
    const md = new File(["# notes"], "notes.md", { type: "text/markdown" });
    const png = new File(["x"], "shot.png", { type: "image/png" });
    fireEvent.drop(container.querySelector(".ph-dropzone")!, {
      dataTransfer: { files: [md, png] },
    });
    expect(screen.getByText("notes.md")).toBeTruthy();
    expect(screen.getByText("shot.png")).toBeTruthy();
    expect(screen.getByText("text")).toBeTruthy();
    expect(screen.getByText("image")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Brief"), { target: { value: "Ship it" } });
    fireEvent.click(screen.getByRole("button", { name: "Start" }));
    expect(onCreate).toHaveBeenCalledWith("Ship it", [md, png], undefined);
  });

  it("adds a pasted file and can remove an attached file", () => {
    const { container } = render(<ProductHome {...base} onCreate={() => {}} />);
    const png = new File(["x"], "pasted.png", { type: "image/png" });
    fireEvent.paste(screen.getByLabelText("Brief"), { clipboardData: { files: [png] } });
    expect(screen.getByText("pasted.png")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Remove pasted.png" }));
    expect(screen.queryByText("pasted.png")).toBeNull();
    expect(container.querySelector(".ph-files")).toBeNull();
  });

  it("seeds from an existing initiative (brownfield --from)", () => {
    const onCreate = vi.fn();
    render(
      <ProductHome
        {...base}
        initiatives={[{ id: "ini-1", title: "Usage-based billing", description: "x", status: "approved" }]}
        onCreate={onCreate}
      />,
    );
    fireEvent.change(screen.getByLabelText("Brief"), { target: { value: "Add CSV export" } });
    fireEvent.change(screen.getByLabelText("Seed from an existing initiative (optional)"), {
      target: { value: "ini-1" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Start" }));
    expect(onCreate).toHaveBeenCalledWith("Add CSV export", [], "ini-1");
  });
});
