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

    def sh(self, args, timeout=20, stderr=False):
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

    def test_a_readable_journal_without_a_pass_is_critical_when_a_network_needs_enforcing(self):
        f = FakeSys()
        f.cmds.append((["journalctl", "-u", "stayconnect-netd"], 0, '{"time":"2026-11-06T11:40:00Z","msg":"other"}\n'))
        f.sqls.append((r"FROM public.guest_networks WHERE enabled\"?$|guest_networks WHERE enabled$", [["2"]]))
        self.assertEqual(by_key(run(f))["enf:pass"].status, om.CRIT)
        f.sqls[0] = (r"FROM public.guest_networks WHERE enabled\"?$|guest_networks WHERE enabled$", [["0"]])
        self.assertEqual(by_key(run(f))["enf:pass"].status, om.PASS)
        f.sqls.clear()
        self.assertEqual(by_key(run(f))["enf:pass"].status, om.NV)

    def test_an_optional_unit_switched_off_is_not_a_fault_but_one_switched_on_and_down_is(self):
        def svc(unit, active, filestate):
            f = FakeSys()
            f.cmds.append((["systemctl", "show"], 0, f"Id={unit}.service\nActiveState={active}\nSubState=dead\nNRestarts=0\nUnitFileState={filestate}\n"))
            return by_key(run(f))[f"svc:{unit}"].status
        self.assertEqual(svc("stayconnect-pmsd", "inactive", "disabled"), om.PASS)
        self.assertEqual(svc("kea-dhcp4-server", "inactive", "disabled"), om.PASS)
        self.assertEqual(svc("stayconnect-pmsd", "inactive", "enabled"), om.CRIT)
        self.assertEqual(svc("stayconnect-scd", "inactive", "disabled"), om.CRIT)

    def test_an_unused_redis_container_is_a_parity_warning_and_the_database_is_required(self):
        f = FakeSys()
        f.cmds.append((["docker", "ps"], 0, "stayconnect-pg running\nstayconnect-redis running\n"))
        r = by_key(run(f))
        self.assertEqual((r["container:stayconnect-pg"].status, r["container:stayconnect-redis"].status), (om.PASS, om.WARN))
        f.cmds[0] = (["docker", "ps"], 0, "stayconnect-pg exited\n")
        r = by_key(run(f))
        self.assertEqual((r["container:stayconnect-pg"].status, r["container:stayconnect-redis"].status), (om.CRIT, om.PASS))


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

    def test_a_revoked_licence_is_critical_and_an_unreadable_revocation_list_unverified(self):
        f = FakeSys()
        payload = {"license_id": "L1", "status": "active", "valid_until": "2027-12-01T23:59:59Z", "offline_grace_days": 7}
        f.files["/etc/stayconnect/license/current.json"] = json.dumps({"payload": json.dumps(payload)})
        f.files["/etc/stayconnect/license/state.json"] = json.dumps({"last_cloud_validation": "2026-11-06T09:00:00Z"})
        f.files["/etc/stayconnect/license/revoked.json"] = '["L0", "L1"]'
        self.assertEqual(by_key(run(f))["lic:valid"].status, om.CRIT)
        f.files["/etc/stayconnect/license/revoked.json"] = '["L0"'
        self.assertEqual(by_key(run(f))["lic:valid"].status, om.NV)
        f.files["/etc/stayconnect/license/revoked.json"] = '["L0"]'
        self.assertEqual(by_key(run(f))["lic:valid"].status, om.PASS)

    def test_licence_capacity_uses_the_legacy_cap_and_treats_none_as_unlimited(self):
        def cap(payload, online=95):
            f = FakeSys()
            p = {"license_id": "L1", "status": "active", "valid_until": "2027-12-01T23:59:59Z", "offline_grace_days": 7, **payload}
            f.files["/etc/stayconnect/license/current.json"] = json.dumps({"payload": json.dumps(p)})
            f.files["/etc/stayconnect/license/state.json"] = json.dumps({"last_cloud_validation": "2026-11-06T09:00:00Z"})
            f.sqls.append((r"state IN \('active', 'PENDING_ENFORCEMENT'\)", [[str(online)]]))
            mon = om.Monitor(f, "quick")
            mon.check_licence({})
            return by_key(mon)["lic:capacity"].status
        self.assertEqual(cap({"limits": {"max_concurrent_guest_sessions": 100}}), om.WARN)
        self.assertEqual(cap({"max_concurrent_online_guests": 0, "limits": {}}), om.PASS)
        self.assertEqual(cap({"max_concurrent_online_guests": 100}, online=100), om.CRIT)

    def test_unreadable_disks_are_unverified_not_zero(self):
        f = FakeSys()
        f.files.update({"/proc/stat": "cpu 100 0 100 800 0 0 0 0\n", "/proc/meminfo": "MemAvailable: 4000000 kB\n", "/proc/loadavg": "0.5 0.4 0.3 1/200 1\n"})
        mon = run(f)
        self.assertEqual(by_key(mon)["res:disk:/"].status, om.NV)
        self.assertEqual(mon.m["disk"], "?")

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


class ReviewFindings(unittest.TestCase):
    def test_a_service_error_burst_clears_when_its_journal_is_clean(self):
        f = FakeSys()
        burst = "".join(json.dumps({"_SYSTEMD_UNIT": "stayconnect-pmsd.service", "MESSAGE": '{"level":"ERROR","msg":"x"}'}) + "\n" for _ in range(25))
        f.cmds.append((["journalctl", "-u", "stayconnect-*"], 0, burst))
        state = {}
        mon = run(f, state=state)
        om.Incidents(state, NOW).apply(mon.results)
        self.assertEqual(by_key(mon)["errors:stayconnect-pmsd"].status, om.WARN)
        f.cmds[0] = (["journalctl", "-u", "stayconnect-*"], 0, "")
        mon = run(f, state=state)
        self.assertEqual(by_key(mon)["errors:stayconnect-pmsd"].status, om.PASS)
        _, rec = om.Incidents(state, NOW).apply(mon.results)
        self.assertEqual([k for k, _ in rec], ["errors:stayconnect-pmsd"])

    def test_unreadable_leases_are_not_empty_pools(self):
        f = FakeSys()
        f.files["/etc/kea/kea-dhcp4.conf"] = json.dumps({"Dhcp4": {"subnet4": [
            {"interface": "br-g95", "authoritative": True, "pools": [{"pool": "10.0.0.1 - 10.0.0.100"}]}]}})
        self.assertEqual(by_key(run(f))["dhcp:pools"].status, om.NV)

    def test_only_rotated_lease_copies_are_not_current_evidence(self):
        f = FakeSys()
        f.files["/etc/kea/kea-dhcp4.conf"] = json.dumps({"Dhcp4": {"subnet4": [
            {"interface": "br-g95", "authoritative": True, "pools": [{"pool": "10.0.0.1 - 10.0.0.100"}]}]}})
        f.files["/var/lib/kea/kea-leases4.csv.1"] = "address,hwaddr\n"
        self.assertEqual(by_key(run(f))["dhcp:pools"].status, om.NV)

    def test_a_removed_network_retires_its_bridge_incident(self):
        f = FakeSys()
        f.cmds += [(["ip", "-j", "link"], 0, "[]"), (["ip", "-j", "route"], 0, "[]")]
        f.sqls.append((r"FROM public.guest_networks WHERE enabled", [["br-g90"]]))
        state = {"incidents": {"net:bridge:br-old": {"first": NOW.isoformat(), "last": NOW.isoformat(), "count": 3, "streak": 3,
                                                     "raised": True, "status": om.CRIT, "text": "client network br-old missing", "area": "network"}}}
        mon = run(f, state=state)
        _, rec = om.Incidents(state, NOW).apply(mon.results)
        self.assertEqual([k for k, _ in rec], ["net:bridge:br-old"])

    def missed(self, returned_seconds_ago, lease_ip="10.0.0.9"):
        f = FakeSys()
        exp = int(NOW.timestamp()) + 28800 - returned_seconds_ago
        f.files["/var/lib/kea/kea-leases4.csv"] = f"address,hwaddr\n{lease_ip},aa:bb:cc:dd:ee:09,,28800,{exp},1,0,0,h,0,\n"
        f.sqls += [(r"SELECT DISTINCT device_id::text FROM iam_v2.sessions WHERE ended IS NULL", []),
                   (r"subnet_cidr", [["br-g95", "10.0.0.0/24"], ["br-g90", "10.90.0.0/24"]]),
                   (r"extract\(epoch FROM s.ended\)", [["dev-1", "aa:bb:cc:dd:ee:09", "10.0.0.9", "br-g95", "11-06 10:00", str(int(NOW.timestamp()) - 7200)]]),
                   (r".", [])]
        mon = om.Monitor(f, "deep")
        items = [i for i in mon.devices({}) if i["issue"].startswith("back on the network")]
        return items, by_key(mon)["reconnect:missed"].status

    def test_a_device_back_for_over_ten_minutes_on_its_network_is_a_missed_reconnection(self):
        items, st = self.missed(1800)
        self.assertEqual((len(items), st), (1, om.WARN))

    def test_a_device_that_just_returned_is_not_yet_a_missed_reconnection(self):
        self.assertEqual(self.missed(120), ([], om.PASS))

    def test_a_device_back_on_another_network_is_not_a_missed_reconnection(self):
        self.assertEqual(self.missed(1800, lease_ip="10.90.0.9"), ([], om.PASS))

    def test_reconnection_server_failures_are_told_from_ineligibility(self):
        f = FakeSys()
        line = lambda reason: '{"time":"2026-11-06T11:55:00Z","level":"INFO","msg":"phase3 rejoin: not eligible","reason":"%s"}' % reason
        out = "\n".join([line("no_rejoinable_entitlement")] * 5 + [line("candidate: ERROR: permission denied (SQLSTATE 42501)"), line("enforcement_not_confirmed")])
        f.cmds.append((["journalctl", "-u", "stayconnect-scd"], 0, out))
        f.sqls.append((r".", []))
        r = by_key(run(f))["auth:rejoin-errors"]
        self.assertEqual(r.status, om.WARN)
        self.assertIn("2 automatic reconnection(s) failed", r.text)
        f.cmds[0] = (["journalctl", "-u", "stayconnect-scd"], 0, "\n".join([line("no_rejoinable_entitlement")] * 9))
        self.assertEqual(by_key(run(f))["auth:rejoin-errors"].status, om.PASS)

    def test_a_safety_backup_does_not_hide_the_age_of_the_newest_backup(self):
        f = FakeSys()
        f.files["/opt/stayconnect/backups/db/db-safety-20261101T000000Z.sql.gz.age"] = "x"
        f.files["/opt/stayconnect/backups/db/db-20261106T060000Z.sql.gz.age"] = "x"
        r = by_key(run(f, "deep"))["backup:fresh"]
        self.assertEqual(r.status, om.PASS)
        self.assertIn("6 h old", r.text)
        del f.files["/opt/stayconnect/backups/db/db-20261106T060000Z.sql.gz.age"]
        self.assertEqual(by_key(run(f, "deep"))["backup:fresh"].status, om.CRIT)

    def test_warning_cpu_samples_do_not_satisfy_critical_persistence(self):
        state = {}
        cpu = lambda busy: [om.Result("res:cpu", "resources", om.WARN if busy > 85 else om.PASS, "", persist=2),
                            om.Result("res:cpu-critical", "resources", om.CRIT if busy > 95 else om.PASS, "", persist=3)]
        for busy in (90, 90, 97):
            active, _ = om.Incidents(state, NOW).apply(cpu(busy))
        self.assertEqual(sorted(r.status for r, _ in active), [om.WARN])

    def test_a_full_licence_is_not_a_reconnection_server_error(self):
        f = FakeSys()
        line = '{"time":"2026-11-06T11:55:00Z","msg":"phase3 rejoin: not eligible","reason":"capacity: licensed concurrent-guest capacity reached"}'
        f.cmds.append((["journalctl", "-u", "stayconnect-scd"], 0, "\n".join([line] * 4)))
        f.sqls.append((r".", []))
        self.assertEqual(by_key(run(f))["auth:rejoin-errors"].status, om.PASS)

    def test_a_decommissioned_pms_connection_retires_its_incidents_but_missing_evidence_does_not(self):
        inc = lambda: {"pms:link:OLD": {"first": NOW.isoformat(), "last": NOW.isoformat(), "count": 4, "streak": 4, "raised": True,
                                        "status": om.CRIT, "text": "PMS OLD DISCONNECTED", "area": "pms"}}
        f = FakeSys()
        f.sqls += [(r"pms_interface_runtime", [["CHR", "CONNECTED", "-1", "60"]]), (r"posting_outbox", [["0", "0", "0"]])]
        state = {"incidents": inc()}
        mon = run(f, state=state)
        _, rec = om.Incidents(state, NOW).apply(mon.results, mon.complete)
        self.assertEqual([k for k, _ in rec], ["pms:link:OLD"])
        g = FakeSys()
        state = {"incidents": inc()}
        mon = run(g, state=state)
        active, rec = om.Incidents(state, NOW).apply(mon.results, mon.complete)
        self.assertEqual(rec, [])
        self.assertIn("pms:link:OLD", [r.key for r, _ in active])

    def test_an_unreadable_session_count_keeps_the_consistency_incident(self):
        f = FakeSys()
        f.cmds.append((["journalctl", "-u", "stayconnect-netd"], 0,
                       '{"time":"2026-11-06T11:59:00Z","msg":"phase3: pass","sessions":400,"failed":0,"pass_ms":300}\n'))
        state = {"incidents": {"enf:consistency": {"first": NOW.isoformat(), "last": NOW.isoformat(), "count": 2, "streak": 2,
                                                   "raised": True, "status": om.WARN, "text": "gap", "area": "enforcement"}}}
        mon = run(f, state=state)
        self.assertEqual(by_key(mon)["enf:consistency"].status, om.NV)
        active, rec = om.Incidents(state, NOW).apply(mon.results, mon.complete)
        self.assertEqual(rec, [])

    def test_an_enforcement_outage_does_not_resolve_other_enforcement_incidents(self):
        f = FakeSys()
        f.cmds.append((["journalctl", "-u", "stayconnect-netd"], 0, '{"time":"2026-11-06T11:00:00Z","msg":"other"}\n'))
        f.sqls.append((r"guest_networks WHERE enabled$", [["1"]]))
        state = {"incidents": {"enf:consistency": {"first": NOW.isoformat(), "last": NOW.isoformat(), "count": 2, "streak": 2,
                                                   "raised": True, "status": om.WARN, "text": "gap", "area": "enforcement"}}}
        mon = run(f, state=state)
        active, rec = om.Incidents(state, NOW).apply(mon.results, mon.complete)
        self.assertEqual(rec, [])
        self.assertEqual(sorted(r.key for r, _ in active if r.key.startswith("enf:")), ["enf:consistency", "enf:pass"])

    def test_the_headline_is_not_pass_while_evidence_is_missing(self):
        mon = run(FakeSys())
        nv = sum(1 for r in mon.results if r.status == om.NV)
        self.assertIn("status=NOT_VERIFIED", om.summary_line(mon, [], nv))
        self.assertIn("OVERALL: NOT_VERIFIED", om.render_report(mon, [], [], {}))
        self.assertEqual(om.overall([], 0), om.PASS)


class DatabaseDiagnostics(unittest.TestCase):
    def mon(self, settings, log):
        f = FakeSys()
        f.sqls += [(r"FROM pg_stat_activity\),", [["10", "100", "0", "0", "3"]]),
                   (r"FROM pg_settings", [[k, v] for k, v in settings.items()]),
                   (r"FROM pg_class WHERE oid IN", [["16420", "entitlements"], ["16500", "sessions"]])]
        f.cmds.append((["docker", "logs"], 0, log))
        state = {}
        mon = om.Monitor(f, "quick")
        mon.check_database(state)
        return by_key(mon), state

    def test_lock_waits_are_counted_once_by_table_and_logging_privacy_is_guarded(self):
        log = "\n".join([
            "2026-11-06 11:00:01.000 UTC [101] LOG:  process 101 still waiting for RowExclusiveLock on tuple (3,5) of relation 16420 of database 5 after 1000.1 ms",
            "2026-11-06 11:00:02.000 UTC [102] LOG:  process 102 still waiting for ShareLock on transaction 9911 after 1000.2 ms",
            "2026-11-06 11:00:03.000 UTC [103] LOG:  process 103 still waiting for RowExclusiveLock on tuple (1,1) of relation 16420 of database 5 after 1001.0 ms",
            "2026-11-06 11:00:04.000 UTC [101] LOG:  process 101 acquired RowExclusiveLock on tuple (3,5) of relation 16420 of database 5 after 1500.0 ms"])
        r, state = self.mon({"log_lock_waits": "on", "log_error_verbosity": "terse", "log_min_error_statement": "panic"}, log)
        self.assertEqual(r["db:lock-wait-logging"].status, om.PASS)
        self.assertEqual(r["db:log-privacy"].status, om.PASS)
        self.assertIn("3 lock wait(s)", r["db:lock-waits-logged"].text)
        self.assertEqual(state["lock_waits"]["2026-11-06"], {"entitlements": 2, "transaction": 1})
        self.assertEqual(state["lock_waits_until"], "2026-11-06T12:00:00Z")
        r, _ = self.mon({"log_lock_waits": "off", "log_error_verbosity": "default", "log_min_error_statement": "error"}, "")
        self.assertEqual((r["db:lock-wait-logging"].status, r["db:log-privacy"].status), (om.WARN, om.WARN))


class RealSubprocess(unittest.TestCase):
    def test_a_log_written_to_stderr_is_read_when_asked(self):
        s = om.Sys(om.CFG)
        script = ["sh", "-c", "echo out; echo 'LOG:  process 1 still waiting for ShareLock on transaction 2' >&2"]
        rc, out = s.sh(script, stderr=True)
        self.assertEqual(rc, 0)
        self.assertIn("still waiting for", out)
        self.assertIn("out", out)
        rc, out = s.sh(script)
        self.assertNotIn("still waiting for", out)


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


class ConsoleSnapshot(unittest.TestCase):
    def test_the_console_gets_no_address_and_a_quick_run_carries_the_last_deep_sections(self):
        deep = run(FakeSys(), "deep")
        deep.report["devices"] = [dict(device="ab12cd34", mac="…:ee:ff", ip="10.3.4.5", network="br-guest", issue="IP conflict", cause="c",
                                       confidence="confirmed", severity="MEDIUM", first="11-06 10:00", last="11-06 11:00", count=2,
                                       status="ONGOING", action="a", seen_since="11-06 10:00")]
        active = [(om.Result("svc:x", "services", om.WARN, "x down"), {"first": NOW.isoformat(), "last": NOW.isoformat(), "count": 3})]
        snap = om.console_snapshot(deep, active, {"resolved": [{"key": "k", "text": "t", "first": "a", "cleared": "b", "status": om.WARN}]}, {})
        self.assertEqual(snap["overall"], om.WARN)
        self.assertEqual(snap["incidents"][0]["count"], 3)
        self.assertNotIn("ip", snap["deep"]["devices"][0])
        self.assertNotIn("10.3.4.5", json.dumps(snap))
        self.assertEqual(snap["deep"]["devices"][0]["mac"], "…:ee:ff")
        quick = om.console_snapshot(run(FakeSys()), [], {}, json.loads(json.dumps(snap)))
        self.assertEqual(quick["mode"], "quick")
        self.assertEqual(quick["deep"]["at"], snap["deep"]["at"])
        self.assertEqual(quick["incidents"], [])

    def test_the_snapshot_is_group_readable_and_never_world_readable(self):
        import grp
        import stat
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            old = dict(om.CFG)
            om.CFG.update(snapshot=os.path.join(d, "console", "health.json"), snapshot_group=grp.getgrgid(os.getgid()).gr_name)
            try:
                om.write_console_snapshot(run(FakeSys()), [], {})
                mode = stat.S_IMODE(os.stat(om.CFG["snapshot"]).st_mode)
            finally:
                om.CFG.clear()
                om.CFG.update(old)
            self.assertEqual(mode, 0o640)


class Locking(unittest.TestCase):
    def test_a_deep_run_waits_for_a_running_quick_run_and_a_quick_run_skips(self):
        import fcntl
        import tempfile
        import threading
        import time as _t
        with tempfile.TemporaryDirectory() as d:
            old = dict(om.CFG)
            om.CFG.update(state=d, log=os.path.join(d, "log"))
            calls = []
            orig = om.Monitor.run
            om.Monitor.run = lambda self, state: calls.append(self.mode) or set()
            try:
                held = open(os.path.join(d, ".lock"), "w")
                fcntl.flock(held, fcntl.LOCK_EX)
                om.main(["quick", "--stdout"])
                self.assertEqual(calls, [], "a quick run did not skip a held lock")
                threading.Timer(1.5, lambda: fcntl.flock(held, fcntl.LOCK_UN)).start()
                t0 = _t.monotonic()
                om.main(["deep", "--stdout"])
                self.assertEqual(calls, ["deep"])
                self.assertGreaterEqual(_t.monotonic() - t0, 1.0)
            finally:
                held.close()
                om.Monitor.run = orig
                om.CFG.clear()
                om.CFG.update(old)


@unittest.skipUnless(os.environ.get("OG_TEST_PSQL"), "OG_TEST_PSQL not set")
class EveryQueryRunsOnTheCurrentSchema(unittest.TestCase):
    def test_all_queries(self):
        cfg = dict(om.CFG, psql=os.environ["OG_TEST_PSQL"])

        class DbOnly(om.Sys):
            def sh(self, args, timeout=20, stderr=False):
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
