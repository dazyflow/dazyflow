// Vitest setup shared by all tests. Adds jest-dom matchers (toBeInTheDocument,
// toHaveTextContent, …) and clears the DOM + mocks between tests so component
// tests don't leak state into each other.
import "@testing-library/jest-dom/vitest";
import { afterEach, vi } from "vitest";
import { cleanup } from "@testing-library/react";

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});
