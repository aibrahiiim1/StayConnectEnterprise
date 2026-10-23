// Kept so bookmarks of the old address still work.

import { permanentRedirect } from "next/navigation";

// A server redirect (308) answers before any client code runs, so there is no interim page.
export default function LicenseMoved(): never {
  permanentRedirect("/appliance");
}
