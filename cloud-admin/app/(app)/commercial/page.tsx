import { permanentRedirect } from "next/navigation";

// RETIRED. Plans, subscription terms and limit overrides were the old way to say what a customer may run; the
// signed appliance license replaced all three. The page was off the menu but still live by URL, with unlabelled
// forms that could change terms nothing reads any more. An old bookmark now lands where that answer lives today.
export default function CommercialPage() {
  permanentRedirect("/licenses");
}
