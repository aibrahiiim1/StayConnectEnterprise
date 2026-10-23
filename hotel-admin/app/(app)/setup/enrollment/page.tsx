import { permanentRedirect } from "next/navigation";

// Server-side 308 so bookmarked URLs resolve before any client code runs.
export default function ActivationMoved(): never {
  permanentRedirect("/appliance");
}
