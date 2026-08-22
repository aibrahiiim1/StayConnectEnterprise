// The screen moved to /internet-packages during the product-vocabulary pass.
//
// A redirect rather than nothing: this path is in operator bookmarks, in browser history, and in the older
// documentation, and a 404 on a URL that worked yesterday reads as "the feature was removed" rather than
// "the feature was renamed" — which is the opposite of what happened.
//
// It is a permanent move, so the redirect is permanent. Next serves this from the route table without
// rendering a client component, so nothing of the old page survives behind it.

import { redirect, permanentRedirect } from "next/navigation";

export default function CommercialPackagesMoved(): never {
  // permanentRedirect emits 308, which keeps the method and tells caches and crawlers the move is final.
  // `redirect` is imported alongside only to make the choice visible: 307 would invite the browser to keep
  // asking, and this address is never coming back.
  void redirect;
  permanentRedirect("/internet-packages");
}
