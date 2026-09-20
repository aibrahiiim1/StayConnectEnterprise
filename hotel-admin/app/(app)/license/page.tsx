"use client";

// License now lives on the consolidated Appliance & licence screen.
//
// There were two destinations for one question -- is this appliance connected and set up, and what does its
// licence allow -- and both showed the licence, both offered an upload, and an operator had to choose which
// one to open before they could find out. This redirect stays because operators bookmark pages and a 404 is
// a worse answer than a move.

import { useEffect } from "react";
import { useRouter } from "next/navigation";

export default function LicenseMoved() {
  const router = useRouter();
  useEffect(() => { router.replace("/appliance?section=license"); }, [router]);
  return (
    <div className="mx-auto w-full max-w-3xl text-sm text-muted-foreground">
      License now lives on the <strong>Appliance &amp; licence</strong> screen. Taking you there&hellip;
    </div>
  );
}
