# CHR Protel PMS commissioning on the test appliance (2026-11-01)

Product-Owner authorization of 2026-11-01 (D88): real, non-financial PMS commissioning on the test appliance 172.21.96.97
against the CHR Protel FIAS endpoint 150.0.0.18:5003. Room-charge posting stayed off: no `PHASE4` transmit setting exists
on the appliance. Release under test: `onegate-appliance-f5347cf23afc`, read back from the installed files (all eight
binaries embed `f5347cf23afcef696b084de091fc0ce04534e5dd`, unmodified). No guest data is reproduced here: stays are counted,
never named.

## Starting point: configured, not working

The CHR interface was published and ACTIVE in the Admin Console. Nothing connected:
- `pmsd` was disabled and had never run;
- its environment was the installer's empty dark template;
- the Client network had no Room sign-in routing decision;
- Room sign-in was switched off in the site's Sign-in methods;
- the checkout-grace catalogue was absent.

## What was done

1. `pmsd.env`:
   - the Phase-3 connector, ingest and checkout-grace flags;
   - the least-privilege `svc_pmsd` DSN from `/etc/stayconnect/.dsn`;
   - two newly generated HMAC keys, with a root-only vault copy on the appliance;
   - mode 0640 `root:stayconnect-pmsd`.

   Then the grace preflight was run (`bootstrap_emergency_grace`, after which health was `OK`), and `pmsd` was enabled.
2. Routing: Client Wi-Fi was set `DEDICATED` to CHR, by preview and apply.
3. Sign-in methods: Room sign-in switched on, in mode `either` (last name or reservation number).
4. Three connector defects found on the real link were fixed, tested and deployed (`9f17d554`, then `f5347cf2`):
   - the idle keepalive;
   - the periodic sweep;
   - the commit-time wake-up of the plan producer.

   One console defect was fixed too: readiness now honours the site switch. Details are in
   [the commissioning runbook](../runbooks/PMS-Interface-Commissioning.md).

## Runtime evidence

| Check | Result |
|---|---|
| Interface health | `CONNECTED / CONTINUOUS / IN_SYNC`. Sync stage `COMPLETE`, 587 of 587 rooms enumerated by every reconciliation |
| Roster | about 420 in-house stays mirrored from CHR. **0 duplicate stay identities** after five reconnects and resyncs |
| Live processing | check-ins, room changes and check-outs are applied as they happen. From 08:10 to 09:25: 2 check-ins, 5 changes and 12 check-outs, ending at 411 in house and 50 checked out |
| First-connection backlog | Protel replayed the live records it had queued. Check-outs and changes for stays never seen are held for review (234 + 42), not invented; 4 changes target ended stays |
| Reconnect | `pmsd` restart: reconnected, resynced to `IN_SYNC`, same stay counts, no stale stays, one socket to CHR |
| Link stability, before the fix | dropped every ~90 s idle (`TRANSPORT_TIMEOUT`), then reconnected and resynced every ~2.5 min |
| Link stability, after the fix | 5 minutes and then 12 minutes with no reconnect (generation constant). Heartbeat age ≤ 65 s against a 90 s bound |
| Evidence freshness | a periodic sweep about every 510 s. The oldest in-house evidence peaked at 606 s against the 690 s ceiling |
| Persistence | three upgrades kept `pmsd.env`, kept the unit enabled and restarted `pmsd` |
| Room sign-in (real) | from a client on the Client Wi-Fi bridge, using the room and reservation number of an in-house stay chosen at random (PO authorised any room). A wrong reservation is refused. The portal answers `ok`, the session is active and belongs to that stay, the kernel authorises the client and HTTPS reaches the Internet: **7 of 7**, on two stays, including one more than 11.5 minutes after the restart |
| Admin Console | interface health (`/pms-interfaces/{id}/health`) reports the runtime axes, freshness and `room_auth_ready`. Routing readiness reports the site switch |
| Clean-up | the test sessions were disconnected through the console route. Their free 1-hour packages are left to expire, because an End would block those guests from re-acquiring them |

## Not verified

- a full host reboot of the appliance;
- a PMS outage longer than the reconnects exercised here;
- Room charge (posting), which is not enabled;
- the guest-facing pages in a real phone browser (the portal API was driven by the client namespace).
