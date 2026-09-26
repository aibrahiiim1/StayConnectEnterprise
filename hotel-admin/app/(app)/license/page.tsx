// The licence now lives on the consolidated Appliance & licence screen.
//
// There were two destinations for one question -- is this appliance connected and set up, and what does its
// licence allow -- and both showed the licence, both offered an upload, and an operator had to choose which
// one to open before they could find out. This redirect stays because operators bookmark pages and a 404 is
// a worse answer than a move.

import { permanentRedirect } from "next/navigation";

// A server redirect (308), like /commercial-packages: the old address answers before any client code runs, so
// there is no interim "Taking you there…" page and no flash of it. The move is permanent.
export default function LicenseMoved(): never {
  permanentRedirect("/appliance?section=license");
}
