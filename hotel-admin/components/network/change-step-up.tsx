"use client";

// APPLY, KEEP AND ROLL BACK A CLIENT-NETWORK CHANGE ASK FOR THE OPERATOR'S PASSWORD.
//
// These three change what every client network on the appliance forwards, so the server requires the
// operator's password for each (edged netApply / confirmRevision / rollbackRevision), exactly as it already did
// for a WAN / LAN change. The password is typed into a masked field inside a confirmation that says what will
// happen -- never a browser prompt. One hook, so every Networking screen asks the same way.

import * as React from "react";
import { ConfirmDialog } from "@/components/ui/dialog";
import { errMsg } from "@/lib/utils";

export type NetworkChangeKind = "apply" | "confirm" | "rollback";

/** A refused password (401 reauth_required): the dialog stays open and says so; nothing was changed. */
export function isReauth(e: unknown): boolean {
  const x = e as { status?: number; code?: string; body?: { error?: string } } | null;
  return !!x && x.status === 401 && (x.code === "reauth_required" || x.body?.error === "reauth_required");
}

function stepUpMessage(e: unknown): string {
  return isReauth(e) ? "That password is not correct. Nothing was changed." : errMsg(e);
}

const COPY: Record<NetworkChangeKind, { title: string; description: string; label: string; consequences: string[] }> = {
  apply: {
    title: "Apply the staged client-network changes?",
    description: "The staged changes go live on the appliance as soon as you confirm.",
    label: "Apply changes",
    consequences: [
      "Clients on a network that changes may lose their connection while it is rebuilt.",
      "If nobody keeps the change before the timer runs out, the appliance puts the previous configuration back on its own.",
    ],
  },
  confirm: {
    title: "Keep this configuration?",
    description: "The applied change becomes the active configuration and will no longer roll back on its own.",
    label: "Keep this change",
    consequences: [],
  },
  rollback: {
    title: "Roll back this configuration?",
    description: "The appliance puts the previous client-network configuration back now.",
    label: "Roll back now",
    consequences: ["Clients on a network this change created lose their connection."],
  },
};

export function useNetworkChangeStepUp() {
  const [req, setReq] = React.useState<{ kind: NetworkChangeKind; run: (password: string) => Promise<void> } | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  const ask = React.useCallback((kind: NetworkChangeKind, run: (password: string) => Promise<void>) => {
    setError(null);
    setReq({ kind, run });
  }, []);

  const copy = req ? COPY[req.kind] : COPY.apply;
  const dialog = (
    <ConfirmDialog
      open={!!req}
      onOpenChange={(v) => { if (!v && !busy) setReq(null); }}
      title={copy.title}
      description={copy.description}
      consequences={copy.consequences.length ? copy.consequences : undefined}
      confirmLabel={copy.label}
      confirmVariant={req?.kind === "rollback" ? "danger" : "primary"}
      busy={busy}
      error={error}
      requirePassword
      passwordLabel="Confirm your password"
      onConfirm={async ({ password }) => {
        if (!req) return;
        if (!password) { setError("Confirm your password to change the network configuration."); return; }
        setBusy(true); setError(null);
        try { await req.run(password); setReq(null); }
        catch (e) { setError(stepUpMessage(e)); }
        finally { setBusy(false); }
      }}
    />
  );
  return { ask, dialog };
}
