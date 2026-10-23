import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup, configure } from "@testing-library/react";

// A longer wait ceiling absorbs parallel-worker load without delaying tests that settle sooner.
configure({ asyncUtilTimeout: 5000 });

afterEach(() => cleanup());
