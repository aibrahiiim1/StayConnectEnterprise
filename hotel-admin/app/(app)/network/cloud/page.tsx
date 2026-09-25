"use client";

// "Cloud connection" was a page per backend component, and there is no second subsystem to administer.
//
// Under the licensing-only model (T0071) the link to Central exists to register the appliance, issue its
// certificate, carry the licence and enforce it. The NATS transport is never opened and the telemetry outbox
// is stopped, both by decision. Every fact the page showed was therefore either licence state, certificate
// health or appliance identity — and all three now have exactly one home on Appliance & licence.
//
// This redirect stays because operators bookmark pages and because a 404 is a worse answer than a move. It goes
// straight to the licence section rather than through /license, which is itself only a redirect.

import { useEffect } from "react";
import { useRouter } from "next/navigation";

export default function CloudConnectionMoved() {
  const router = useRouter();
  useEffect(() => {
    router.replace("/appliance?section=license");
  }, [router]);
  return (
    <p className="text-sm text-muted-foreground" role="status">
      Cloud connection now lives on the <strong>Appliance &amp; licence</strong> screen, because licensing is what the
      link to Central is for. Taking you there…
    </p>
  );
}
