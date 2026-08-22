import { test, expect, type Page, type Route } from "@playwright/test";

// Browser-level E2E for the Phase-4 (DARK) financial operator surface. edged is fully mocked at the network
// layer -- no real backend, no database, no production data, no PMS and no payment provider. The Next server
// under test runs with NEXT_PUBLIC_PHASE4_ADMIN=1, a TEST-only flag-ON profile that is never the deployed
// dark bundle.
//
// What a browser proves that jsdom cannot: the recovery screen is genuinely OPERABLE end to end -- an
// operator can read the situation, reach a conclusion, submit it and see the queue change -- and that what
// the network actually receives is what the screen said it would send.

type Mutations = { method: string; path: string; body: any }[];

const HEALTH_HELD = {
  outbox_queued: 3, outbox_in_flight: 0, outbox_held_recovery: 3, outbox_oldest_age_seconds: 5400,
  postings_unknown: 1, review_queue_open: 2, review_oldest_age_seconds: 7200,
  payments_created: 1, payments_pending: 0, payments_unknown: 1, payments_oldest_age_seconds: 900,
  settlements_required: 1, settlements_in_progress: 0, settlements_manual_review: 1, settlements_failed: 0,
  recovery_active: true, recovery_epoch: 2, recovery_holds_open: 2,
  payment_account_configured: true, provider_egress_enabled: false,
  status: "HELD",
  reasons: ["FINANCIAL_RECOVERY_MODE", "UNKNOWN_OUTCOMES_AWAITING_REVIEW"],
};

const HOLDS = [
  { hold_id: "h1", work_kind: "PAYMENT_TRANSACTION", work_id: "w1", held_status: "PENDING",
    amount_minor: 1500, currency: "USD", held_at: new Date().toISOString() },
  { hold_id: "h2", work_kind: "POSTING_OUTBOX", work_id: "w2", held_status: "QUEUED",
    amount_minor: null, currency: "", held_at: new Date().toISOString() },
];

const REVIEW_ROW = {
  posting_id: "p1", pms_interface_id: "i1", execution_state: "UNKNOWN", amount_minor: 1500,
  currency: "USD", currency_exponent: 2, latest_attempt_no: 1, latest_p_number: "42",
  latest_pa_as_status: null, outbox_state: "HELD_RECOVERY", review_version: 0,
  terminal_review_action: null, awaiting_manual_review: true, created_at: new Date().toISOString(),
};
const REVIEW_DETAIL = {
  posting: REVIEW_ROW,
  pinned_evidence: { settlement_id: "s1", purchase_id: "pu1", stay_id: "st1", folio_id: "f1",
    connector_kind: "protel-fias", folio_identity_strategy: "UNIQUE_PER_STAY",
    interface_lifecycle_state: "ACTIVE", settlement_status: "REQUIRED",
    purchase_state: "AWAITING_SETTLEMENT" },
  attempts: [{ attempt_no: 1, p_number: "42", rn: "101", g_number: "7", outcome: "UNKNOWN",
    pa_as_status: null, sent_at: new Date().toISOString(), response_at: null }],
  review: { history: [], version: 0, terminal_action: null, escalation_count: 0,
    retry_authorized_attempt_no: null, retry_authorization_consumed: false },
  diagnostics: { attempt_count: 1, unknown_attempt_count: 1, has_unknown_history: true,
    interface_freshness_block: null },
  available_actions: ["CONFIRM_POSTED", "ESCALATE"],
  evidence_contract: { source_types: ["PMS_SCREEN", "PROVIDER_DASHBOARD"] },
  limitations: ["Programmatic PMS reversal is capability=false in v1."],
};
const SETTLEMENT = { settlement_id: "s1", purchase_id: "pu1", method: "ONLINE_PAYMENT", status: "SETTLED",
  purchase_state: "GRANTED", amount_minor: 1000, currency: "USD", currency_exponent: 2 };
const PAYMENT = { payment_id: "x1", transaction_type: "CHARGE", status: "CAPTURED",
  provider: "test-double", amount_minor: 1000, currency: "USD", currency_exponent: 2,
  parent_transaction_id: null };

async function installBackend(
  page: Page,
  mutations: Mutations,
  opts: { holdsAfter?: any[]; zeroQueue?: any[] } = {},
) {
  let resolved = 0;
  let authorized = 0;
  // The middleware gates every page on a session cookie. Setting one is what makes these specs exercise the
  // real routes rather than the login redirect; the value is never validated here because edged is mocked.
  await page.context().addCookies([
    { name: "sc_edge_session", value: "e2e-test", url: "http://127.0.0.1:3123" },
  ]);
  await page.route("**/api/edge/v1/**", async (route: Route) => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname.replace("/api/edge/v1", "");
    const method = req.method();

    if (method !== "GET") {
      mutations.push({ method, path, body: req.postDataJSON?.() ?? null });
    }

    const json = (body: any, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });

    switch (true) {
      case path === "/auth/whoami":
        return json({ email: "ops@test.local", roles: ["site_admin", "payments_operator"], operator_id: "op-1" });
      case path === "/financial-ops/health":
        return json({ health: HEALTH_HELD });
      case path === "/financial-ops/recovery":
        return json({
          recovery: {
            Epoch: 2, Reason: "RESTORE_DETECTED", Active: true,
            HeldTotal: HOLDS.length, HeldOpen: HOLDS.length - resolved,
            EnteredAt: new Date().toISOString(), ReleasedAt: "",
          },
        });
      case path === "/financial-ops/recovery/holds":
        return json({ holds: resolved > 0 ? (opts.holdsAfter ?? HOLDS.slice(1)) : HOLDS });
      case /\/financial-ops\/recovery\/holds\/.+\/resolve$/.test(path):
        resolved += 1;
        return json({ resolved: true });
      case path === "/financial-ops/recovery/release":
        return json({ released: true, epoch: 2 });
      case path === "/financial-ops/recovery/zero-attempt":
        return json({
          queue: authorized > 0 ? [] : (opts.zeroQueue ?? []),
          limit: 200,
          evidence_contract: { source_types: ["PMS_FOLIO_INSPECTION", "PMS_REPORT"] },
          note: "Authorizing a retry sends nothing: it authorises exactly ONE future attempt.",
          eligibility: "Eligible only once the hold was reconciled as CONFIRMED_NOT_COMPLETED.",
        });
      case /\/financial-ops\/recovery\/zero-attempt\/.+\/authorize$/.test(path):
        authorized += 1;
        return json({ action_id: "a1", authorized_attempt_no: 1, actor: "op-1", transmitted: false });
      case path === "/financial-review/actions":
        return json({ actions: [
          { action: "CONFIRM_POSTED", terminal: true, needs_evidence: true, accepts_amount: false,
            summary: "the folio already shows it" },
          { action: "CREATE_REVERSAL", terminal: true, needs_evidence: true, accepts_amount: true,
            summary: "record a reversal ledger row" },
          { action: "ESCALATE", terminal: false, needs_evidence: false, accepts_amount: false,
            summary: "someone else must look" },
        ] });
      case path === "/financial-review/queue":
        return json({ queue: [REVIEW_ROW] });
      case path === "/financial-review/postings/p1":
        return json(REVIEW_DETAIL);
      case path === "/financial-ops/settlements":
        return json({ settlements: [SETTLEMENT] });
      case path === "/financial-ops/settlements/s1":
        return json({ settlement: SETTLEMENT, payments: [PAYMENT], available_actions: [],
          note: "Refund and chargeback initiation are NOT available from this surface in Phase 4." });
      default:
        return json({ error: "not_found" }, 404);
    }
  });
}

test.describe("Phase 4 financial operator surface", () => {
  test("financial health leads with HELD and explains why, without naming any provider", async ({ page }) => {
    const mutations: Mutations = [];
    await installBackend(page, mutations);
    await page.goto("/financial-health");

    // exact: the metric label "Held (recovery)" also contains the word, and the assertion is about the badge
    await expect(page.getByText("HELD", { exact: true })).toBeVisible();
    await expect(
      page.getByText(/money movement is deliberately held until every item in flight has been reconciled/i),
    ).toBeVisible();
    await expect(page.getByText("Disabled (DARK)")).toBeVisible();

    // No real provider may be named anywhere on a screen that has not integrated one.
    const body = (await page.locator("body").innerText()).toLowerCase();
    for (const name of ["stripe", "adyen", "checkout.com", "paypal", "braintree"]) {
      expect(body).not.toContain(name);
    }
    // A read-only screen performs no writes.
    expect(mutations).toHaveLength(0);
  });

  test("an operator can reconcile a held item end to end, and the queue shrinks", async ({ page }) => {
    const mutations: Mutations = [];
    await installBackend(page, mutations);
    await page.goto("/financial-recovery");

    // Scoped to the content region on purpose. getByText() matches a SUBSTRING and is CASE-INSENSITIVE, so
    // "FINANCIAL RECOVERY" also matched the navigation link "Financial recovery" -- two elements, and Playwright
    // fails strict mode. Which one resolved first depended on render timing, so the same commit produced both a
    // passing and a failing run. The banner is what this asserts, and the banner is inside <main>; the nav link
    // is outside it. Nothing in the product changed to accommodate the test.
    await expect(page.getByRole("main").getByText("FINANCIAL RECOVERY")).toBeVisible();
    // The ambiguity itself, asserted so it cannot come back silently: exactly ONE banner inside the content
    // region, and exactly one navigation LINK for the same destination. The sidebar item is now "Recovery"
    // under a "Charges" group — the group supplies the word the label used to carry — so the nav matcher is
    // anchored to that label rather than to /financial recovery/. The property under test is unchanged: if
    // the banner is ever rendered twice, or the nav moves inside <main>, this fails here with a clear reason
    // instead of surfacing as an intermittent strict-mode violation.
    await expect(page.getByRole("main").getByText("FINANCIAL RECOVERY")).toHaveCount(1);
    await expect(page.getByRole("navigation").getByRole("link", { name: /^recovery$/i })).toHaveCount(1);
    await expect(page.getByText(/2 items still to reconcile/i)).toBeVisible();
    await expect(page.getByText(/guest internet access is unaffected/i)).toBeVisible();

    await page.getByLabel(/your password/i).fill("hunter2");
    await page.getByLabel(/conclusion for this payment/i).selectOption("CONFIRMED_NOT_COMPLETED");
    await page
      .getByLabel(/evidence for this decision/i)
      .first()
      .fill("checked the provider dashboard; no charge exists");
    await page.getByRole("button", { name: /^record$/i }).first().click();

    await expect(page.getByText(/recorded\. nothing was re-sent\./i)).toBeVisible();

    const resolve = mutations.find((m) => m.path.endsWith("/resolve"));
    expect(resolve).toBeTruthy();
    expect(resolve!.body).toMatchObject({
      resolution: "CONFIRMED_NOT_COMPLETED",
      note: "checked the provider dashboard; no charge exists",
      password: "hunter2",
    });
    // the author is the session, never the request
    expect(Object.keys(resolve!.body)).not.toContain("actor");
  });

  test("release appears only once nothing is left to reconcile", async ({ page }) => {
    const mutations: Mutations = [];
    await installBackend(page, mutations, { holdsAfter: [] });
    await page.goto("/financial-recovery");

    await expect(page.getByRole("button", { name: /release financial recovery/i })).toHaveCount(0);

    await page.getByLabel(/your password/i).fill("hunter2");
    await page.getByLabel(/conclusion for this payment/i).selectOption("CONFIRMED_COMPLETED");
    await page.getByLabel(/evidence for this decision/i).first().fill("provider dashboard shows the capture");
    await page.getByRole("button", { name: /^record$/i }).first().click();

    const release = page.getByRole("button", { name: /release financial recovery/i });
    await expect(release).toBeVisible();
    await page.getByLabel(/why is it safe to resume/i).fill("every held item reconciled against the provider");
    await release.click();
    await expect(page.getByText(/money movement has resumed/i)).toBeVisible();
  });

  // ---------------------------------------------------------------- the zero-attempt path
  //
  // The one state a restore can produce that no other screen can decide: a charge held BEFORE anything was
  // sent. It has no attempt, so the Manual Review queue -- which keys on attempts -- cannot list it. What a
  // browser proves that jsdom cannot is that the whole authorization is genuinely completable: real form
  // controls, a real submit, and the exact body that leaves the page.
  const ZERO_ROW = {
    posting_id: "11111111-1111-1111-1111-111111111111", outbox_id: "o1", pms_interface_id: "i1",
    amount_minor: 2500, currency: "USD", currency_exponent: 2,
    hold_id: "h9", hold_resolution: "CONFIRMED_NOT_COMPLETED", retry_authorized_attempt_no: null,
    eligible_for_retry_authorization: true,
  };

  test("an operator can authorize a never-transmitted posting, and nothing is sent", async ({ page }) => {
    const mutations: Mutations = [];
    await installBackend(page, mutations, { zeroQueue: [ZERO_ROW] });
    await page.goto("/financial-recovery");

    await expect(page.getByText(/Never transmitted \(1\)/)).toBeVisible();
    await expect(page.getByText("25.00 USD")).toBeVisible();

    await page.getByLabel(/your password/i).fill("hunter2");
    await page.getByLabel(/why this charge must still go out/i)
      .fill("checked the folio directly; this charge was never posted");
    await page.getByLabel(/evidence source for this posting/i).selectOption("PMS_FOLIO_INSPECTION");
    await page.getByLabel(/reference to that evidence/i).fill("folio 4471");
    await page.getByRole("button", { name: /authorize one attempt/i }).click();

    await expect(page.getByText(/nothing has been sent/i)).toBeVisible();

    const sent = mutations.filter((m) => /zero-attempt/.test(m.path));
    expect(sent).toHaveLength(1);
    expect(sent[0].method).toBe("POST");
    expect(sent[0].path).toBe(
      "/financial-ops/recovery/zero-attempt/11111111-1111-1111-1111-111111111111/authorize",
    );
    expect(sent[0].body).toEqual({
      reason: "checked the folio directly; this charge was never posted",
      evidence: { source_type: "PMS_FOLIO_INSPECTION", reference: "folio 4471" },
      password: "hunter2",
    });
    // the audit author comes from the session, and no amount travels with an authorization
    for (const forbidden of ["actor", "operator_id", "amount_minor", "attempt_no"]) {
      expect(Object.keys(sent[0].body as object)).not.toContain(forbidden);
    }
    // it is offered exactly once
    await expect(page.getByRole("button", { name: /authorize one attempt/i })).toHaveCount(0);
  });

  test("an unreconciled never-transmitted posting is visible but not authorizable", async ({ page }) => {
    await installBackend(page, [], {
      zeroQueue: [{ ...ZERO_ROW, hold_resolution: null, eligible_for_retry_authorization: false }],
    });
    await page.goto("/financial-recovery");

    await expect(page.getByText("NOT YET RECONCILED")).toBeVisible();
    await expect(page.getByText(/Reconcile this item above/i)).toBeVisible();
    await expect(page.getByRole("button", { name: /authorize one attempt/i })).toHaveCount(0);
  });

  test("the zero-attempt section offers no bulk action and no way to send", async ({ page }) => {
    await installBackend(page, [], {
      zeroQueue: [ZERO_ROW, { ...ZERO_ROW, posting_id: "22222222-2222-2222-2222-222222222222" }],
    });
    await page.goto("/financial-recovery");
    await expect(page.getByText(/Never transmitted \(2\)/)).toBeVisible();
    await expect(page.getByRole("button", { name: /authorize one attempt/i })).toHaveCount(2);
    for (const forbidden of [/authorize all/i, /send now/i, /transmit/i, /post now/i]) {
      await expect(page.getByRole("button", { name: forbidden })).toHaveCount(0);
    }
  });

  test("the recovery screen offers no way to replay anything", async ({ page }) => {
    await installBackend(page, []);
    await page.goto("/financial-recovery");
    // Scoped to the content region on purpose. getByText() matches a SUBSTRING and is CASE-INSENSITIVE, so
    // "FINANCIAL RECOVERY" also matched the navigation link "Financial recovery" -- two elements, and Playwright
    // fails strict mode. Which one resolved first depended on render timing, so the same commit produced both a
    // passing and a failing run. The banner is what this asserts, and the banner is inside <main>; the nav link
    // is outside it. Nothing in the product changed to accommodate the test.
    await expect(page.getByRole("main").getByText("FINANCIAL RECOVERY")).toBeVisible();
    for (const forbidden of [/retry/i, /re-?send/i, /replay/i, /force/i]) {
      await expect(page.getByRole("button", { name: forbidden })).toHaveCount(0);
    }
  });

  test("every control in the real accessibility tree has a name", async ({ page }) => {
    await installBackend(page, []);
    await page.goto("/financial-recovery");
    // Wait for the HELD TABLE, not merely the banner. The banner renders as soon as the recovery status
    // arrives, while the controls this test enumerates come from the holds request -- so anchoring on the
    // banner leaves a window in which the accessibility tree is legitimately still empty. On a fast local
    // machine the two responses land together; on a slower runner they do not, and that is a race in the
    // test rather than a defect in the screen.
    await expect(page.getByRole("button", { name: /^record$/i }).first()).toBeVisible();

    for (const role of ["button", "combobox", "textbox"] as const) {
      const controls = page.getByRole(role);
      const n = await controls.count();
      for (let i = 0; i < n; i++) {
        const name = await controls.nth(i).evaluate((el) => {
          const label = el.getAttribute("aria-label");
          if (label) return label;
          const id = el.getAttribute("id");
          if (id) {
            const l = document.querySelector(`label[for="${id}"]`);
            if (l?.textContent?.trim()) return l.textContent.trim();
          }
          return (el.textContent ?? "").trim();
        });
        expect(name, `${role} #${i} has no accessible name`).not.toBe("");
      }
    }
  });

  test("manual review offers only the authorized actions and records a decision", async ({ page }) => {
    const mutations: Mutations = [];
    await installBackend(page, mutations);
    await page.goto("/financial-review");

    await expect(page.getByText(/nobody knows whether the folio was charged/i)).toHaveCount(0);
    await page.getByRole("button", { name: /^review$/i }).first().click();
    await expect(page.getByText(/nobody knows whether the folio was charged/i)).toBeVisible();

    const select = page.getByLabel(/what did you establish/i);
    await expect(select.locator('option[value="CONFIRM_POSTED"]')).toHaveCount(1);
    // available_actions did not include it, so the screen must not offer it
    await expect(select.locator('option[value="CREATE_REVERSAL"]')).toHaveCount(0);

    await select.selectOption("CONFIRM_POSTED");
    await page.getByLabel(/^why$/i).fill("the folio shows the charge");
    await page.getByLabel(/evidence source/i).selectOption("PMS_SCREEN");
    await page.getByLabel(/reference to it/i).fill("folio screen 14:22");
    await page.getByLabel(/your password/i).fill("hunter2");
    await page.getByRole("button", { name: /record decision/i }).click();

    const decision = mutations.find((m) => m.path.endsWith("/actions"));
    expect(decision).toBeTruthy();
    expect(decision!.body).toMatchObject({ action: "CONFIRM_POSTED", expected_version: 0 });
    expect(Object.keys(decision!.body)).not.toContain("actor");
  });

  test("the settlement browser shows the charge and offers no refund", async ({ page }) => {
    await installBackend(page, []);
    await page.goto("/financial-settlements");
    await page.getByRole("button", { name: /^open$/i }).first().click();
    await expect(page.getByText("CAPTURED")).toBeVisible();
    for (const forbidden of [/refund/i, /chargeback/i, /reverse/i]) {
      await expect(page.getByRole("button", { name: forbidden })).toHaveCount(0);
    }
    await expect(page.getByText(/not available from this surface/i)).toBeVisible();
  });
});
