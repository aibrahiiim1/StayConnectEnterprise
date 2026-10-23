// Moved to /internet-packages; kept so old bookmarks resolve.

import { redirect, permanentRedirect } from "next/navigation";

export default function CommercialPackagesMoved(): never {
  // 308 rather than 307: this address never comes back.
  void redirect;
  permanentRedirect("/internet-packages");
}
