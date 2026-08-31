// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Deliberately NOT the recommended set.
//
// `tsc --strict` (plus noUnusedLocals / noUnusedParameters /
// noFallthroughCasesInSwitch) already covers most of what a general TypeScript
// ruleset would flag, and the nine check-*.mjs guards cover the design-system
// rules a type checker cannot see. Turning on the full recommended config over
// a tree this clean produces noise, and a lint run people learn to ignore is
// worse than no lint run.
//
// What is left uncovered is the hooks rules, and nothing else can catch them: a
// stale closure over a missing dependency is well-typed, silent, and shows up
// as a value that is one render behind. This tree has a 5,291-line FlowEditor
// and a 4,224-line SchemaForm, both hook-heavy — exactly where that bug hides.
//
// Add a rule here only when it has caught something real.
import js from "@eslint/js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";

export default tseslint.config(
  { ignores: ["dist", "dist-docs", "node_modules", "src/docs/content"] },
  {
    files: ["src/**/*.{ts,tsx}"],
    languageOptions: {
      parser: tseslint.parser,
      parserOptions: { ecmaVersion: "latest", sourceType: "module" },
    },
    plugins: { "react-hooks": reactHooks },
    rules: {
      "react-hooks/rules-of-hooks": "error",
      "react-hooks/exhaustive-deps": "warn",
    },
  },
);
