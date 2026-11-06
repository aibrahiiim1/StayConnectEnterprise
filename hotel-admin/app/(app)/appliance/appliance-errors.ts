const UNCHANGED = "Nothing was changed.";

function failure(e: unknown): { status?: number; code: string; text: string } {
  const x = e as { status?: number; code?: unknown; message?: unknown } | null;
  return {
    status: typeof x?.status === "number" ? x.status : undefined,
    code: typeof x?.code === "string" ? x.code : "",
    text: typeof x?.message === "string" ? x.message : "",
  };
}

const unreachable = (f: { status?: number; code: string }) =>
  f.status === undefined || f.status >= 500 || f.code === "scd_unreachable";

export function licenceFileError(e: unknown, serial?: string | null): string {
  const f = failure(e);
  const t = f.text.toLowerCase();
  if (f.code === "LICENSE_ROLLBACK_REJECTED" || t.includes("rollback")) {
    return `This licence is not newer than the one already installed, so it was refused. ${UNCHANGED}`;
  }
  if (t.includes("different appliance")) {
    return `This licence was issued for a different appliance. Ask your OneGate vendor for a licence for serial number ${serial || "shown under Technical details"}. ${UNCHANGED}`;
  }
  if (t.includes("vendor public key")) {
    return `This appliance is not activated yet, so it cannot check a licence. Activate it first. ${UNCHANGED}`;
  }
  if (t.includes("empty") || t.includes("not an activation") || t.includes("bad package") || f.code === "bad_request") {
    return `That file is not a licence file. ${UNCHANGED}`;
  }
  if (unreachable(f)) {
    return `The appliance's licensing service did not answer. Try again in a minute. ${UNCHANGED}`;
  }
  return `This licence file was refused: it may be damaged, expired or not issued by your OneGate vendor. Ask them for a new file. ${UNCHANGED}`;
}

export function packageError(e: unknown, serial?: string | null): string {
  const f = failure(e);
  const t = f.text.toLowerCase();
  if (f.code === "removed_from_central" || t.includes("removed from onegate central")) {
    return "This appliance was removed from OneGate Central. To use it again, factory-reset it and have your vendor activate it.";
  }
  if (t.includes("already been used") || t.includes("already consumed")) {
    return `This package has already been used. Ask your OneGate vendor for a new one. ${UNCHANGED}`;
  }
  if (t.includes("no activation request")) {
    return `Download the activation request first, send it to your OneGate vendor, then upload the package they return. ${UNCHANGED}`;
  }
  if (t.includes("different customer or site")) {
    return `This package is for a different hotel or site than this appliance is assigned to. ${UNCHANGED}`;
  }
  if (t.includes("license install failed")) return licenceFileError({ ...f, message: f.text.replace(/^license install failed:\s*/i, "") }, serial);
  if (t.includes("package rejected")) {
    return `This package was not issued for this appliance, or it is no longer valid. Ask your OneGate vendor for a new one. ${UNCHANGED}`;
  }
  if (t.includes("empty") || t.includes("not an activation") || t.includes("bad package")) {
    return `That file is not an activation or licence package. ${UNCHANGED}`;
  }
  if (unreachable(f)) {
    return `The appliance could not apply the package just now. Try again in a minute. ${UNCHANGED}`;
  }
  return `The package could not be applied. Ask your OneGate vendor for a new one. ${UNCHANGED}`;
}

export function requestError(e: unknown): string {
  const f = failure(e);
  if (f.code === "removed_from_central" || f.text.toLowerCase().includes("removed from onegate central")) {
    return packageError(e);
  }
  return "The activation request could not be downloaded. Check the appliance is reachable, then try again.";
}

export function checkNowError(e: unknown): string {
  const f = failure(e);
  if (f.status === 429 || f.code === "rate_limited") return "OneGate Central was checked a moment ago. Wait a minute, then try again.";
  return "The check could not be run. Check the appliance is reachable, then try again.";
}

function stamp(s: string): string {
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleString();
}

export function clockResetError(e: unknown): string {
  const f = failure(e);
  const t = f.text.toLowerCase();
  if (t.includes("correct the clock first") || t.includes("disagrees") || t.includes("differs")) {
    const m = f.text.match(/clock \(([^)]+)\) differs from Central's \(([^)]+)\)/);
    const times = m ? ` The appliance's clock reads ${stamp(m[1])} and OneGate Central's reads ${stamp(m[2])}.` : "";
    return `The appliance's clock still differs from OneGate Central's time.${times} Correct the appliance's clock first, then try again. ${UNCHANGED}`;
  }
  if (t.includes("secure link")) {
    return `This appliance is not connected to OneGate Central, so Central's time cannot be read. Try again once OneGate Central shows Connected. ${UNCHANGED}`;
  }
  if (t.includes("vendor public key")) {
    return `This appliance is not activated yet, so there is no licence clock to reset. ${UNCHANGED}`;
  }
  if (t.includes("reason")) return `Give a reason, then try again. ${UNCHANGED}`;
  if (unreachable(f) || t.includes("central's time") || t.includes("usable time")) {
    return `OneGate Central's time could not be read. Try again in a minute. ${UNCHANGED}`;
  }
  return `The licence clock could not be reset. ${UNCHANGED}`;
}
