"use client";

// Route shell for the Phase-6 (DARK) online-time budget view. edged mounts the endpoint only when the
// Phase-6 aggregate flag is on, and the sessions role matrix decides who may read it; this page renders
// whatever the API is willing to answer.

import { AggregateTimeView } from "@/components/phase6/aggregate-time-view";

export default function OnlineTimePage() {
  return <AggregateTimeView />;
}
