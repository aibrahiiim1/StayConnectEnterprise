import { formatSpeed } from "@/lib/units";

/** "20 / 10 Mbps" — download / upload on one line; units are repeated only when they differ. */
export function formatSpeedPair(downKbps?: number | null, upKbps?: number | null): string {
  const down = formatSpeed(downKbps);
  const up = formatSpeed(upKbps);
  if (down === "Unlimited" && up === "Unlimited") return "Unlimited";
  const [dv, du] = down.split(" ");
  const [uv, uu] = up.split(" ");
  return du && du === uu ? `${dv} / ${uv} ${du}` : `${down} / ${up}`;
}
