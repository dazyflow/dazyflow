// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The searchable IANA picker. What matters here is that it stays a text field
// underneath: the list is a convenience over the ~400 zones the browser knows,
// and the value it writes is always a plain zone name the drop can read.

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string, vars?: Record<string, unknown>) =>
      vars ? k + " " + Object.values(vars).join(" ") : k,
  }),
}));

import { TimezoneField } from "./TimezoneField";

describe("TimezoneField", () => {
  it("shows the stored zone when closed", () => {
    render(<TimezoneField value="Europe/Stockholm" onChange={() => {}} />);
    expect(screen.getByRole("combobox")).toHaveValue("Europe/Stockholm");
  });

  it("filters as you type and picks with a click", async () => {
    const onChange = vi.fn();
    render(<TimezoneField value="UTC" onChange={onChange} />);
    await userEvent.type(screen.getByRole("combobox"), "stockh");

    const options = screen.getAllByRole("option");
    expect(options).toHaveLength(1);
    await userEvent.click(options[0]);
    expect(onChange).toHaveBeenCalledWith("Europe/Stockholm");
  });

  // Underscores are how the tz database writes a two-word city and not how
  // anyone types one.
  it("matches a space against an underscored name", async () => {
    render(<TimezoneField value="UTC" onChange={() => {}} />);
    await userEvent.type(screen.getByRole("combobox"), "new york");
    expect(
      screen.getByRole("option", { name: /America\/New_York/ }),
    ).toBeInTheDocument();
  });

  it("picks the highlighted row with the keyboard", async () => {
    const onChange = vi.fn();
    render(<TimezoneField value="UTC" onChange={onChange} />);
    const input = screen.getByRole("combobox");
    await userEvent.type(input, "tokyo");
    await userEvent.keyboard("{Enter}");
    expect(onChange).toHaveBeenCalledWith("Asia/Tokyo");
  });

  // The escape hatch: a zone this engine's list doesn't carry, or one of the
  // names the drop accepts that no picker can list ("Local"). Losing this
  // would make the picker strictly less capable than the text box it replaced.
  it("commits typed text when nothing matches", async () => {
    const onChange = vi.fn();
    render(<TimezoneField value="UTC" onChange={onChange} />);
    await userEvent.type(screen.getByRole("combobox"), "Local");
    expect(screen.queryAllByRole("option")).toHaveLength(0);
    await userEvent.keyboard("{Enter}");
    expect(onChange).toHaveBeenCalledWith("Local");
  });

  // Half a zone name is not a zone. Committing it on the way out would put a
  // run-time failure into a flow whose author believes it is configured.
  it("reverts to the stored value on blur without committing", async () => {
    const onChange = vi.fn();
    render(<TimezoneField value="Europe/Stockholm" onChange={onChange} />);
    const input = screen.getByRole("combobox");
    await userEvent.type(input, "Asia/To");
    await userEvent.tab();
    expect(onChange).not.toHaveBeenCalled();
    expect(input).toHaveValue("Europe/Stockholm");
  });

  it("says when nothing matches", async () => {
    render(<TimezoneField value="UTC" onChange={() => {}} />);
    await userEvent.type(screen.getByRole("combobox"), "zzzz");
    expect(screen.getByText("schemaForm.tzNoMatch")).toBeInTheDocument();
  });

  // The cap is a rendering limit, not a claim about the list — so it says how
  // many it left out rather than looking like the whole answer.
  it("caps the rows and reports how many were left out", async () => {
    render(<TimezoneField value="UTC" onChange={() => {}} />);
    await userEvent.click(screen.getByRole("combobox"));
    expect(screen.getAllByRole("option").length).toBeLessThanOrEqual(60);
    expect(screen.getByText(/schemaForm.tzMore \d+/)).toBeInTheDocument();
  });

  it("shows each zone's current offset", async () => {
    render(<TimezoneField value="UTC" onChange={() => {}} />);
    await userEvent.type(screen.getByRole("combobox"), "Asia/Tokyo");
    // Tokyo has no daylight saving, so this holds whatever time of year it is.
    expect(screen.getByRole("option", { name: /GMT\+9/ })).toBeInTheDocument();
  });
});
