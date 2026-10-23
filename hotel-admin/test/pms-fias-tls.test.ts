import { describe, expect, it } from "vitest";
import { PROTEL_KIND, protelRevisionBody, valuesFromRevision, type PmsProvider, type PmsProviderField } from "@/lib/api/pms-connections";

// Preserve nested FIAS TLS settings when editing a connection so encryption cannot silently turn off.
const fields: PmsProviderField[] = [
  { key: "endpoint", label: "PMS address", type: "host_port", required: true },
  { key: "tls_enabled", label: "Encrypt", type: "bool", default: false },
  { key: "tls_cert_sha256", label: "Fingerprint", type: "string" },
] as PmsProviderField[];
const protel = { kind: PROTEL_KIND, fields } as unknown as PmsProvider;

describe("Protel FIAS TLS in the connection form", () => {
  it("starts an edit from the stored TLS settings and sends them back", () => {
    const pin = "ab".repeat(32);
    const { values } = valuesFromRevision(protel, {
      config: { endpoint: "10.0.0.5:5010", tls: { enabled: true, cert_sha256: pin } }, source_timezone: "UTC",
    });
    expect(values.tls_enabled).toBe(true);
    expect(values.tls_cert_sha256).toBe(pin);
    const body = protelRevisionBody(values, "UTC", fields);
    expect(body.tls_enabled).toBe(true);
    expect(body.tls_cert_sha256).toBe(pin);
  });

  it("is off for a connection saved without it", () => {
    const { values } = valuesFromRevision(protel, { config: { endpoint: "10.0.0.5:5010" }, source_timezone: "UTC" });
    expect(values.tls_enabled).toBe(false);
  });
});
