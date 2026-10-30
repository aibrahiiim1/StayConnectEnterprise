// Kept so bookmarks of the retired page still work: Checkout Grace and Post-Stay are now After Check-out Access.

import { permanentRedirect } from "next/navigation";

// A server redirect (308) answers before any client code runs, so there is no interim page.
export default function Moved(): never {
  permanentRedirect("/after-checkout-access");
}
