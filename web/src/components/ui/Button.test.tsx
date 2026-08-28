// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Button derives its appearance from semantic props, so what it emits as class
// names IS its contract with the stylesheet — a dropped or renamed class is a
// silently unstyled button, the exact failure check-css-classes.mjs exists to
// catch from the other direction. These cover the composition rules the CSS
// depends on, plus the `filled` modifier, whose whole reason for existing is
// that two Stop buttons had drifted apart without one.
import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { Button } from "./Button";

const cls = (ui: React.ReactElement) =>
  (render(ui).container.firstElementChild as HTMLElement).className.split(" ").filter(Boolean);

describe("Button classes", () => {
  it("emits nothing for the base look, so the common case stays clean", () => {
    // secondary + md are the bare <button> skin and must not add classes.
    expect(cls(<Button>Save</Button>)).toEqual([]);
  });

  it("emits the variant and size names the stylesheet matches on", () => {
    expect(cls(<Button variant="danger" size="sm">Delete</Button>)).toEqual([
      "danger",
      "sm",
    ]);
  });

  it("adds `filled` only when asked, giving .danger.filled its solid red", () => {
    expect(cls(<Button variant="danger">Stop</Button>)).toEqual(["danger"]);
    expect(cls(<Button variant="danger" filled>Stop</Button>)).toEqual([
      "danger",
      "filled",
    ]);
  });

  it("keeps a call-site layout class alongside the derived ones", () => {
    // The editor's Stop is `variant="danger" filled className="run-stop"`;
    // run-stop only holds the width, so all three have to survive together.
    expect(cls(<Button variant="danger" filled className="run-stop">Stop</Button>)).toEqual([
      "danger",
      "filled",
      "run-stop",
    ]);
  });

  it("does not leak modifier props onto the DOM element", () => {
    // A boolean that reaches the DOM becomes an invalid attribute and a React
    // warning; `filled` must be consumed by the component.
    const { container } = render(
      <Button variant="danger" filled>
        Stop
      </Button>,
    );
    const el = container.firstElementChild as HTMLElement;
    expect(el.hasAttribute("filled")).toBe(false);
    expect(el.getAttribute("type")).toBe("button");
  });
});
