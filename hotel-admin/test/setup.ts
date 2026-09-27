import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup, configure } from "@testing-library/react";

// A 46-file jsdom suite runs files in parallel workers. On a loaded machine a screen that renders correctly can
// take longer than Testing Library's default 1 s to settle, and the suite then failed a different handful of
// tests on every run -- none of them wrong when run alone. The wait is a ceiling, not a delay: a screen that
// is ready in 50 ms still passes in 50 ms.
configure({ asyncUtilTimeout: 5000 });

afterEach(() => cleanup());
