// "Cloud connection" was a page per backend component, and there is no second subsystem to administer.
//
// The link to Central exists to register the appliance, issue its certificate, carry the licence and enforce
// it -- nothing else. Every fact the page showed was therefore either licence state, certificate health or
// appliance identity, and all three now have exactly one home on Appliance & licence.
//
// This redirect stays because operators bookmark pages and because a 404 is a worse answer than a move. It goes
// straight to the licence section rather than through /license, which is itself only a redirect.

import { permanentRedirect } from "next/navigation";

// A server redirect (308), like /commercial-packages: the old address answers before any client code runs, so
// there is no interim "Taking you there…" page and no flash of it. The move is permanent.
export default function CloudConnectionMoved(): never {
  permanentRedirect("/appliance");
}
