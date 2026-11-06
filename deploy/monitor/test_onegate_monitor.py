#!/usr/bin/env python3
"""Tests for onegate_monitor. Run: python3 -m unittest deploy/monitor/test_onegate_monitor.py
OG_TEST_PSQL (a psql command line for a database at the current schema) also runs every query the monitor issues."""
import datetime as dt
import json
import os
import re
import sys
import unittest

os.environ["OG_FAST"] = "1"
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import onegate_monitor as om  # noqa: E402

NOW = dt.datetime(2026, 11, 6, 12, 0, tzinfo=dt.timezone.utc)


class FakeSys:
    """Answers only what a test configures; anything else is unavailable evidence."""

    def __init__(self):
        self.files, self.cmds, self.sqls, self.urls = {}, [], [], {}
        self.kea = None

    def now(self):
        return NOW

    def sh(self, args, timeout=20):
        for prefix, rc, out in self.cmds:
            if list(args[:len(prefix)]) == list(prefix):
                return rc, out
        return 1, ""

    def sql(self, query, timeout=30):
        for pattern, rows in self.sqls:
            if re.search(pattern, query, re.S):
                return rows
        raise om.Unavailable("database query failed: not configured in this test")

    def read(self, path, tail=None):
        if path in self.files:
            return self.files[path]
        raise om.Unavailable(f"{path}: missing")

    def glob(self, pattern):
        rx = re.compile("^" + re.escape(pattern).replace(r"\*", "[^/]*") + "$")
        return sorted(p for p in self.files if rx.match(p))

    def http(self, url, timeout=5):
        return self.urls.get(url, (0, "connection refused"))

    def unix_json(self, path, payload, timeout=3):
        if self.kea is None:
            raise om.Unavailable("Kea control socket: missing")
        return self.kea

    def statvfs(self, path):
        raise om.Unavailable(f"{path}: missing")


def run(fake, mode="quick", state=None):
    mon = om.Monitor(fake, mode)
    mon.run(state if state is not None else {})
    return mon


def by_key(mon):
    return {r.key: r for r in mon.results}


class NoEvidenceIsNeverPass(unittest.TestCase):
    def test_an_appliance_that_answers_nothing_reports_no_pass(self):
        mon = run(FakeSys(), "deep")
        passing = [r for r in mon.results if r.status == om.PASS]
        self.assertEqual(passing, [], "a check with no evidence reported PASS")
        self.assertTrue(any(r.status == om.NV for r in mon.results))

    def test_an_unreadable_journal_is_not_verified_not_a_missing_pass(self):
        f = FakeSys()
        mon = run(f)
        self.assertEqual(by_key(mon)["enf:pass"].status, om.NV)

    def test_a_readable_journal_without_a_pass_is_critical(self):
        f = FakeSys()
        f.cmds.append((["journalctl", "-u", "stayconnect-netd"], 0, '{"time":"2026-11-06T11:40:00Z","msg":"other"}\n'))
        self.assertEqual(by_key(run(f))["enf:pass"].status, om.CRIT)


class Thresholds(unittest.TestCase):
    def kea(self, used, size=100):
        f = FakeSys()
        f.files["/etc/kea/kea-dhcp4.conf"] = json.dumps({"Dhcp4": {"subnet4": [
            {"interface": "br-g95", "authoritative": True, "pools": [{"pool": f"10.0.0.1 - 10.0.0.{size}"}]}]}})
        exp = int(NOW.timestamp()) + 3600
        f.files["/var/lib/kea/kea-leases4.csv"] = "address,hwaddr\n" + "".join(
            f"10.0.0.{i},aa:bb:cc:dd:ee:{i:02x},,3600,{exp},1,0,0,host,0,\n" for i in range(1, used + 1))
        return f

    def test_pools(self):
        self.assertEqual(by_key(run(self.kea(50)))["dhcp:pools"].status, om.PASS)
        self.assertEqual(by_key(run(self.kea(90)))["dhcp:pools"].status, om.WARN)
        self.assertEqual(by_key(run(self.kea(97)))["dhcp:pools"].status, om.CRIT)

    def test_a_subnet_that_is_not_authoritative_warns(self):
        f = self.kea(10)
        conf = json.loads(f.files["/etc/kea/kea-dhcp4.conf"])
        conf["Dhcp4"]["subnet4"][0]["authoritative"] = False
        f.files["/etc/kea/kea-dhcp4.conf"] = json.dumps(conf)
        self.assertEqual(by_key(run(f))["dhcp:authoritative"].status, om.WARN)

    def test_stuck_on_wrong_details_is_not_an_issue_but_a_system_refusal_is(self):
        f = FakeSys()
        f.sqls.append((r"GROUP BY 1 HAVING count\(\*\) FILTER", [["aa:bb:cc:dd:ee:01", "CREDENTIAL_MISMATCH", "6"]]))
        f.sqls.append((r"FROM iam_v2.sign_in_attempts WHERE occurred_at > now\(\) - interval '15 minutes' GROUP BY 1", [["CREDENTIAL_MISMATCH", "6"]]))
        self.assertEqual(by_key(run(f))["auth:stuck"].status, om.PASS)
        f.sqls[0] = (r"GROUP BY 1 HAVING count\(\*\) FILTER", [["aa:bb:cc:dd:ee:01", "SERVICE_UNAVAILABLE", "6"]])
        self.assertEqual(by_key(run(f))["auth:stuck"].status, om.WARN)

    def test_licence(self):
        def lic(until, validated):
            f = FakeSys()
            payload = {"status": "active", "valid_until": until, "offline_grace_days": 7, "max_concurrent_online_guests": 100}
            f.files["/etc/stayconnect/license/current.json"] = json.dumps({"payload": json.dumps(payload)})
            f.files["/etc/stayconnect/license/state.json"] = json.dumps({"last_cloud_validation": validated})
            return by_key(run(f))
        r = lic("2027-12-01T23:59:59Z", "2026-11-06T09:00:00Z")
        self.assertEqual((r["lic:valid"].status, r["lic:central"].status), (om.PASS, om.PASS))
        r = lic("2026-11-01T00:00:00Z", "2026-11-04T09:00:00Z")
        self.assertEqual((r["lic:valid"].status, r["lic:central"].status), (om.CRIT, om.WARN))
        r = lic("2026-11-20T00:00:00Z", "2026-10-30T00:00:00Z")
        self.assertEqual((r["lic:valid"].status, r["lic:central"].status), (om.WARN, om.CRIT))

    def test_certificates(self):
        f = FakeSys()
        f.files["/etc/caddy/hotel-admin/hotel-admin.crt"] = "x"
        f.cmds.append((["openssl", "x509"], 0, "notAfter=Nov 10 12:00:00 2026 GMT\n"))
        self.assertEqual(by_key(run(f))["cert:/etc/caddy/hotel-admin/hotel-admin.crt"].status, om.CRIT)
        f.cmds[0] = (["openssl", "x509"], 0, "notAfter=Feb  1 12:00:00 2027 GMT\n")
        self.assertEqual(by_key(run(f))["cert:/etc/caddy/hotel-admin/hotel-admin.crt"].status, om.PASS)

    def test_release_binaries_are_read_from_the_installer_format(self):
        f = FakeSys()
        h1, h2 = "a" * 64, "b" * 64
        f.files["/opt/stayconnect/DEPLOYED_BINARIES.sha256"] = f"# service sha256 rev\nscd {h1} rev x false\nedged {h2} rev x false\n"
        f.cmds.append((["sha256sum"], 0, f"{h1}  /opt/stayconnect/bin/scd\n{h2}  /opt/stayconnect/bin/edged\n"))
        self.assertEqual(by_key(run(f, "deep"))["parity:binaries"].status, om.PASS)
        f.cmds[0] = (["sha256sum"], 0, f"{h1}  /opt/stayconnect/bin/scd\n{'c' * 64}  /opt/stayconnect/bin/edged\n")
        self.assertEqual(by_key(run(f, "deep"))["parity:binaries"].status, om.CRIT)
        f.files["/opt/stayconnect/DEPLOYED_BINARIES.sha256"] = "# nothing listed\n"
        self.assertEqual(by_key(run(f, "deep"))["parity:binaries"].status, om.NV)


class UnitParity(unittest.TestCase):
    def setup(self, installed):
        f = FakeSys()
        rel = "[Unit]\nDescription=OneGate edge proxy\n# note\n[Service]\nExecStart=/usr/bin/caddy run\n"
        f.files["/opt/stayconnect/deploy/systemd/stayconnect-scd.service"] = "[Service]\nExecStart=/opt/stayconnect/bin/scd\n"
        f.files["/etc/systemd/system/stayconnect-scd.service"] = "[Service]\nExecStart=/opt/stayconnect/bin/scd\n"
        f.files["/opt/stayconnect/deploy/caddy/stayconnect-caddy.edge.service"] = rel
        f.files["/etc/systemd/system/stayconnect-caddy.service"] = installed
        return by_key(run(f, "deep"))["parity:units"]

    def test_a_description_only_difference_is_noted_not_raised(self):
        r = self.setup("[Unit]\nDescription=StayConnect edge proxy\n[Service]\nExecStart=/usr/bin/caddy run\n")
        self.assertEqual(r.status, om.PASS)
        self.assertIn("description only differs", r.text)

    def test_a_real_difference_warns(self):
        self.assertEqual(self.setup("[Unit]\nDescription=OneGate edge proxy\n[Service]\nExecStart=/usr/bin/caddy run --old\n").status, om.WARN)


class IncidentLifecycle(unittest.TestCase):
    def test_persistence_dedupe_and_recovery(self):
        state = {}
        r = lambda st: [om.Result("pms:link:CHR", "pms", st, "PMS CHR DISCONNECTED", persist=2)]
        a, rec = om.Incidents(state, NOW).apply(r(om.WARN))
        self.assertEqual((a, rec), ([], []), "raised on the first failing run despite persist=2")
        a, rec = om.Incidents(state, NOW + dt.timedelta(minutes=5)).apply(r(om.WARN))
        self.assertEqual(len(a), 1)
        line = om.issue_lines(a, rec, NOW)[0]
        self.assertIn("since 12:00Z, 2 runs", line)
        a, rec = om.Incidents(state, NOW + dt.timedelta(minutes=10)).apply(r(om.WARN))
        self.assertEqual(len(a), 1, "one incident, not a new one per run")
        a, rec = om.Incidents(state, NOW + dt.timedelta(minutes=15)).apply(r(om.PASS))
        self.assertEqual((len(a), len(rec)), (0, 1))
        self.assertTrue(om.issue_lines(a, rec, NOW)[0].startswith("RECOVERED"))
        a, rec = om.Incidents(state, NOW + dt.timedelta(minutes=20)).apply(r(om.PASS))
        self.assertEqual(rec, [], "recovery reported twice")

    def test_an_unraised_blip_clears_silently(self):
        state = {}
        om.Incidents(state, NOW).apply([om.Result("k", "a", om.WARN, "t", persist=2)])
        a, rec = om.Incidents(state, NOW).apply([om.Result("k", "a", om.PASS, "t", persist=2)])
        self.assertEqual(rec, [])

    def test_a_check_that_did_not_run_keeps_its_incident_and_still_shows_it(self):
        state = {}
        om.Incidents(state, NOW).apply([om.Result("parity:units", "installation", om.WARN, "differs")])
        a, rec = om.Incidents(state, NOW + dt.timedelta(minutes=5)).apply([om.Result("svc:scd", "services", om.PASS, "ok")])
        self.assertEqual(rec, [])
        self.assertIn("parity:units", state["incidents"])
        self.assertEqual([r.key for r, _ in a], ["parity:units"])
        self.assertIn("last checked 12:00Z", om.issue_lines(a, rec, NOW)[0])
        self.assertEqual(state["incidents"]["parity:units"]["count"], 1, "a carried incident is not counted as a new run")


class Output(unittest.TestCase):
    def test_masking_keeps_two_octets(self):
        self.assertEqual(om.mask_mac("AA:BB:CC:DD:EE:FF"), "…:ee:ff")
        self.assertEqual(om.mask_mac(None), "unknown")

    def test_summary_keeps_the_fields_the_operators_read(self):
        mon = run(FakeSys())
        line = om.summary_line(mon, [], 3)
        for field in ("signin=", "online=", "netd_sessions=", "cpu=", "pools:", "clients:", "rejoined_15m=", "status=", "not_verified=3", "run_ms="):
            self.assertIn(field, line)

    def test_report_renders_without_data(self):
        mon = run(FakeSys(), "deep")
        text = om.render_report(mon, [], [], {})
        self.assertIn("OVERALL:", text)
        self.assertIn("NOT VERIFIED", text)


@unittest.skipUnless(os.environ.get("OG_TEST_PSQL"), "OG_TEST_PSQL not set")
class EveryQueryRunsOnTheCurrentSchema(unittest.TestCase):
    def test_all_queries(self):
        cfg = dict(om.CFG, psql=os.environ["OG_TEST_PSQL"])

        class DbOnly(om.Sys):
            def sh(self, args, timeout=20):
                raise om.Unavailable("not on an appliance")

            def read(self, path, tail=None):
                raise om.Unavailable("not on an appliance")

            def glob(self, pattern):
                return []

            def http(self, url, timeout=5):
                return 0, ""

            def unix_json(self, path, payload, timeout=3):
                raise om.Unavailable("not on an appliance")

            def statvfs(self, path):
                raise om.Unavailable("not on an appliance")

        mon = om.Monitor(DbOnly(cfg), "deep", cfg)
        mon.check_database({})
        mon.check_pms({})
        mon.check_signin({})
        mon.check_integrity({})
        mon.analytics()
        mon.breakdowns()
        mon.m["leases"] = {}
        mon.devices({})
        failed = [r for r in mon.results if "query failed" in r.text]
        self.assertEqual(failed, [])


if __name__ == "__main__":
    unittest.main()
