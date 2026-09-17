"use client";

// "Cloud connection" was a page per backend component, and there is no second subsystem to administer.
//
// Under the licensing-only model (T0071) the link to Central exists to register the appliance, issue its
// certificate, carry the licence and enforce it. The NATS transport is never opened and the telemetry outbox
// is stopped, both by decision. Every fact the page showed was therefore either licence state, certificate
// health or appliance identity — and all three now have exactly one home on the License screen.
//
// This redirect stays because operators bookmark pages and because a 404 is a worse answer than a move.

import { useEffect } from "react";
import { useRouter } from "next/navigation";

export default function CloudConnectionMoved() {
  const router = useRouter();
  useEffect(() => {
    router.replace("/license");
  }, [router]);
  return (
    <div className="mx-auto w-full max-w-7xl text-sm text-muted">
      Cloud connection now lives on the <strong>License</strong> screen, because licensing is what the link to
      Central is for. Taking you there…
    </div>
  );
}
