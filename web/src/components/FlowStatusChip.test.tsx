import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { FlowStatusChip } from "./FlowStatusChip";

// i18n is stubbed to echo the key, so assertions are stable without loading
// the translation bundle.
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

describe("FlowStatusChip", () => {
  it("renders the status label, tooltip, and a11y role for each state", () => {
    for (const status of ["live", "manual", "paused", "needs_publish"] as const) {
      const { unmount } = render(<FlowStatusChip status={status} />);
      const chip = screen.getByRole("status");
      expect(chip).toHaveClass(`flow-status-${status}`);
      expect(chip).toHaveAttribute("title", `flowStatus.${status}.tip`);
      expect(chip).toHaveTextContent(`flowStatus.${status}.label`);
      unmount();
    }
  });

  it("applies the size modifier class", () => {
    render(<FlowStatusChip status="live" size="sm" />);
    expect(screen.getByRole("status")).toHaveClass("flow-status-sm");
  });
});
