// The Hotel Admin certificate check's own reason, for the Admin Console (network/certificate).
/** The validator's reason from its last output line ("INVALID: <reason>" or "OK (renewal due: <reason>)"). */
export function certCheckDetail(tail: string | undefined): string {
  const lines = (tail ?? "").split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
  for (let i = lines.length - 1; i >= 0; i--) {
    const l = lines[i];
    let m = /INVALID:\s*(.+)$/.exec(l);
    if (m) return m[1].replace(/\.$/, "");
    m = /^OK \(renewal due: (.+)\)$/.exec(l);
    if (m) return `Renewal is due (${m[1]}); the daily renewal will replace it.`;
  }
  return "";
}
