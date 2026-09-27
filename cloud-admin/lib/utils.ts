import { clsx, type ClassValue } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";
import type { ApiError } from "./api";

// THE ONEGATE TYPE ROLES ARE FONT SIZES, AND tailwind-merge MUST BE TOLD SO. Out of the box it reads any
// unknown `text-*` as a text COLOUR, so `cn("text-caption", "text-muted-foreground")` silently dropped the size
// and kept the colour -- the role never reached the page. Registering the roles in the font-size group makes
// a size and a colour coexist, and two sizes resolve to the last one, as they should.
const twMerge = extendTailwindMerge({
  extend: {
    classGroups: {
      "font-size": [
        { text: ["2xs", "title", "metric", "subtitle", "headline", "emphasis", "body", "label", "caption", "micro", "nano"] },
      ],
    },
  },
});

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// errMsg turns any thrown value into a string, appending the server trace id from ApiError when present. Use
// <ErrorBanner err={...} /> when you can store the raw error.
export function errMsg(e: unknown): string {
  if (!e) return "";
  if (typeof e === "object" && e !== null && "traceId" in e && "message" in e) {
    const ae = e as ApiError;
    return ae.traceId ? `${ae.message} (trace ${ae.traceId})` : ae.message;
  }
  if (e instanceof Error) return e.message;
  return String(e);
}
