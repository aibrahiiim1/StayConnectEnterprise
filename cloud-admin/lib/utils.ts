import { clsx, type ClassValue } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

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
