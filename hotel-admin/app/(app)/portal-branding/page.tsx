"use client";

import { useEffect, useState } from "react";
import { api, Whoami, PortalBranding } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { canWrite } from "@/lib/roles";
import { errMsg } from "@/lib/utils";

export default function PortalBrandingPage() {
  const [text, setText] = useState("");
  const [roles, setRoles] = useState<string[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [loaded, setLoaded] = useState(false);

  const writable = canWrite("portal-branding", roles);

  async function load() {
    try {
      const doc = await api.get<PortalBranding>("/portal-branding");
      setText(JSON.stringify(doc ?? {}, null, 2));
      setLoaded(true);
    } catch (e) { setErr(errMsg(e)); }
  }
  useEffect(() => {
    load();
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
  }, []);

  async function onSave() {
    setErr(null); setMsg(null);
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch {
      setErr("Invalid JSON — fix the syntax before saving."); return;
    }
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
      setErr("Branding must be a JSON object."); return;
    }
    setBusy(true);
    try {
      await api.put("/portal-branding", parsed);
      setMsg("Saved.");
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  return (
    <div className="p-6 max-w-4xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Site</div>
          <h1 className="text-2xl font-semibold">Portal branding</h1>
        </div>
        {writable && <Button disabled={busy || !loaded} onClick={onSave}>{busy ? "Saving…" : "Save"}</Button>}
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}
      {msg && <div className="text-ok text-sm mb-4">{msg}</div>}

      <Card>
        <CardHeader><CardTitle>Branding document (JSON)</CardTitle></CardHeader>
        <CardBody>
          <textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            readOnly={!writable}
            spellCheck={false}
            className="w-full h-[28rem] rounded-md bg-panel2 border border-border p-3 text-sm font-mono focus:outline-none focus:ring-2 focus:ring-brand/40"
          />
          <p className="text-xs text-muted mt-2">
            Free-form document (logo URL, terms &amp; conditions, languages, colors). Edited as raw JSON.
          </p>
        </CardBody>
      </Card>
    </div>
  );
}
