// Preserve bookmarked links by redirecting directly to the consolidated Appliance & licence screen.

import { permanentRedirect } from "next/navigation";

export default function CloudConnectionMoved(): never {
  permanentRedirect("/appliance");
}
