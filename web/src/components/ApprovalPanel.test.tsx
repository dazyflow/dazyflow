import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));
vi.mock("../auth", () => ({
  useAuth: () => ({ token: "tok-123" }),
}));
const approveNode = vi.fn().mockResolvedValue({});
vi.mock("../api", () => ({
  api: { approveNode: (...args: unknown[]) => approveNode(...args) },
}));

import { ApprovalPanel } from "./ApprovalPanel";

describe("ApprovalPanel", () => {
  beforeEach(() => approveNode.mockClear());

  it("renders the prompt and the approve/reject controls", () => {
    render(<ApprovalPanel runID="run-1" nodeID="n1" prompt="Ship it?" />);
    expect(screen.getByText("Ship it?")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "inspector.approve" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "inspector.reject" })).toBeInTheDocument();
  });

  it("calls approveNode with the decision + comment on approve", async () => {
    render(<ApprovalPanel runID="run-1" nodeID="n1" />);
    await userEvent.type(screen.getByRole("textbox"), "looks good");
    await userEvent.click(screen.getByRole("button", { name: "inspector.approve" }));
    expect(approveNode).toHaveBeenCalledWith("tok-123", "run-1", "n1", "approve", "looks good");
  });

  it("sends reject with an empty comment as undefined", async () => {
    render(<ApprovalPanel runID="run-9" nodeID="n2" />);
    await userEvent.click(screen.getByRole("button", { name: "inspector.reject" }));
    expect(approveNode).toHaveBeenCalledWith("tok-123", "run-9", "n2", "reject", undefined);
  });
});
