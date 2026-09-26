import { permanentRedirect } from "next/navigation";

// RETIRED. A customer's entitlement is its signed appliance license, not a plan subscription, yet this page's
// "Switch to this plan" still posted to the old endpoint. An old bookmark now lands on Licenses instead.
export default function SubscriptionPage() {
  permanentRedirect("/licenses");
}
