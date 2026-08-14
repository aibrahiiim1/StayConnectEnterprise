#!/usr/bin/env python3
"""ISOLATION REGRESSION for the adversarial mutation suite.

WHAT THIS PROVES, AND WHY IT EXISTS.

The mutation suite used to inject its defects into the REAL governance/project-state.json in the active
checkout and undo them in a `finally`. That is correct only if nothing ever interrupts it and only one copy
ever runs. Both assumptions failed inside a single correction round:

  * a runner that outlived its wrapper kept mutating while a second run started, and each restored the bytes
    IT had captured -- so the later restore silently reverted legitimate corrections. The canonical file was
    left with `latest_transition_id: T0008` and, on another occasion, `phases["1A"].status: NOT_STARTED` and
    an artifact-registry entry with its `removal_gate` blanked;
  * the corruption did not look like a broken test. It looked like a broken repository.

The suite now mutates a disposable sandbox. This test is the proof of that property, written so that it
FAILS if the suite is ever pointed back at the checkout:

  A. CONCURRENCY -- several suites are launched at once, deliberately overlapping. Afterwards the canonical
     project-state.json must be byte-identical and `git status --porcelain` must be unchanged.
  B. INTERRUPTION -- a suite is started and killed while it is mid-matrix. Same two assertions. This is the
     case the old `finally` could never cover, because a killed process does not run one.
  C. FAIL CLOSED -- with sandbox creation forced to fail, the suite must exit non-zero and mutate nothing,
     rather than falling back to the checkout.

Every case compares the same two things: the SHA-256 of governance/project-state.json, and the full
`git status --porcelain` of the repository. The second matters as much as the first -- a suite that restored
the file but left an unrelated path dirty would still have written to a tree somebody is working in.

Usage:  python tools/tests/project_state_validator/run_isolation_regression.py
"""
import hashlib
import io
import os
import shutil
import subprocess
import sys
import tempfile
import time

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
SUITE = os.path.join("tools", "tests", "project_state_validator", "run_mutations.py")
STATE = os.path.join(ROOT, "governance", "project-state.json")

passed = 0
failed = 0


def ok(msg):
    global passed
    print("  [PASS] %s" % msg)
    passed += 1


def bad(msg, detail=""):
    global failed
    print("  [FAIL] %s" % msg)
    if detail:
        print("         %s" % detail)
    failed += 1


def state_sha():
    with open(STATE, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def tree_status():
    r = subprocess.run(["git", "-C", ROOT, "status", "--porcelain"], capture_output=True, text=True)
    return r.stdout


def spawn(env_extra=None, max_cases="2"):
    """A short child run.

    Two cases is deliberate. What these children have to do is WRITE CONCURRENTLY while another writer is
    active -- that is the shape that corrupted the checkout twice -- and two mutations exercise it fully.
    Coverage of the matrix is not this test's job and is not reduced by it: the authoritative gate runs the
    complete matrix with --require-full, which refuses to start while MUTATION_MAX_CASES is set.

    `-u` because the parent reads the child's output line by line to learn when it is safely inside the
    matrix; a buffered child would be killed before it had built anything, and the test would pass while
    proving nothing.
    """
    env = dict(os.environ)
    env["MUTATION_MAX_CASES"] = max_cases
    if env_extra:
        env.update(env_extra)
    return subprocess.Popen([sys.executable, "-u", SUITE], cwd=ROOT, env=env,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)


def wait_for_sandbox(proc, timeout=120):
    """Read the child's output until it names its sandbox, so the kill lands INSIDE the matrix rather than
    before the sandbox exists -- which would test nothing."""
    deadline = time.time() + timeout
    while time.time() < deadline:
        line = proc.stdout.readline()
        if not line:
            if proc.poll() is not None:
                return None
            continue
        if "sandbox:" in line:
            path = line.split("sandbox:", 1)[1].strip()
            time.sleep(8)                    # let it get properly into the matrix
            return path
    return None


def assert_untouched(label, sha0, status0):
    sha1, status1 = state_sha(), tree_status()
    if sha1 != sha0:
        bad("%s: governance/project-state.json is unchanged" % label,
            "sha %s -> %s" % (sha0[:16], sha1[:16]))
        return False
    if status1 != status0:
        added = set(status1.splitlines()) - set(status0.splitlines())
        bad("%s: the working tree is unchanged" % label,
            "new entries: %s" % (sorted(added)[:4] or "(ordering changed)"))
        return False
    ok("%s: canonical state and working tree are byte-identical" % label)
    return True


def main():
    print("=== isolation regression for the adversarial mutation suite ===")
    print("    repo: %s" % ROOT)
    sha0, status0 = state_sha(), tree_status()
    print("    baseline project-state sha: %s" % sha0[:16])

    # ---- A. concurrent runners ---------------------------------------------------------------------------
    print("\n== A. three suites run CONCURRENTLY (the exact shape that corrupted the tree) ==")
    procs = [spawn() for _ in range(3)]
    outs = []
    for p in procs:
        outs.append(p.communicate()[0])
    rcs = [p.returncode for p in procs]
    print("    exit codes: %s" % rcs)
    assert_untouched("A", sha0, status0)
    # Each run must also have actually done its work rather than bailing out early for an unrelated reason.
    if all("sandbox:" in (o or "") for o in outs):
        ok("A: every concurrent run built its own sandbox")
    else:
        bad("A: at least one run did not report a sandbox", (outs[0] or "")[:200])
    if all(rc == 0 for rc in rcs):
        ok("A: every concurrent run completed successfully")
    else:
        bad("A: a concurrent run failed", "exit codes %s\n%s" % (rcs, (outs[0] or "")[-400:]))

    # ---- B1. SIGTERM: handlers DO run ---------------------------------------------------------------------
    print("\n== B1. a suite is terminated with SIGTERM (handlers run) ==")
    sha0, status0 = state_sha(), tree_status()
    p = spawn(max_cases="40")
    sandbox = wait_for_sandbox(p, timeout=180)
    if sandbox:
        ok("B1: the run reached the matrix with sandbox %s" % os.path.basename(os.path.dirname(sandbox)))
    else:
        bad("B1: the run never reported a sandbox")
    p.terminate()                                   # SIGTERM on POSIX; CTRL_BREAK-equivalent on Windows
    p.communicate()
    assert_untouched("B1", sha0, status0)
    if sandbox and not os.path.isdir(sandbox):
        ok("B1: the sandbox was removed by the signal handler")
    elif sandbox:
        # Not fatal for the property that matters, and said plainly rather than glossed.
        print("  [NOTE] B1: the sandbox survived SIGTERM on this platform; the checkout is still untouched,")
        print("         and the startup sweep is what removes it (proved in B3).")

    # ---- B2. FORCED KILL: handlers CANNOT run ---------------------------------------------------------------
    #
    # SIGKILL (POSIX) and TerminateProcess (Windows, which is what Popen.kill() does) run NO handler, NO
    # atexit hook and NO `finally`. So the claim here is deliberately narrow: the CHECKOUT is unchanged,
    # because nothing was ever written to it. Sandbox cleanup is NOT claimed for this case -- it cannot be,
    # by construction -- and B3 shows what actually collects it.
    print("\n== B2. a suite is FORCE-KILLED (SIGKILL / TerminateProcess -- no handler can run) ==")
    sha0, status0 = state_sha(), tree_status()
    p = spawn(max_cases="40")
    sandbox2 = wait_for_sandbox(p, timeout=180)
    if sandbox2:
        ok("B2: the run reached the matrix")
    else:
        bad("B2: the run never reported a sandbox")
    p.kill()
    p.communicate()
    assert_untouched("B2", sha0, status0)
    if sandbox2 and os.path.isdir(sandbox2):
        ok("B2: the sandbox survived the forced kill, exactly as documented -- no cleanup is claimed here")
    elif sandbox2:
        ok("B2: the sandbox is gone (the OS reclaimed it); the checkout was untouched either way")

    # ---- B3. the INDEPENDENT sweep collects what a killed process could not -----------------------------------
    print("\n== B3. the next run sweeps a sandbox left by a force-killed run ==")
    stale = tempfile.mkdtemp(prefix="psmut-")
    old_time = time.time() - (8 * 60 * 60)
    os.utime(stale, (old_time, old_time))
    out = subprocess.run([sys.executable, "-u", SUITE], cwd=ROOT,
                         env={**os.environ, "MUTATION_MAX_CASES": "1"},
                         capture_output=True, text=True).stdout
    if not os.path.isdir(stale):
        ok("B3: a stale sandbox was removed by the next run's startup sweep")
    else:
        bad("B3: the stale sandbox survived the sweep", stale)
        shutil.rmtree(stale, ignore_errors=True)
    if "swept" in out:
        ok("B3: the sweep reports what it collected")
    else:
        bad("B3: the sweep was silent", out[:200])

    # ---- C. fail closed --------------------------------------------------------------------------------------
    print("\n== C. sandbox creation forced to FAIL -- the suite must refuse, not fall back ==")
    sha0, status0 = state_sha(), tree_status()
    # PATH without git: `git clone --shared` cannot run, so build_sandbox() raises.
    broken = dict(os.environ)
    broken["PATH"] = os.path.join(ROOT, "no-such-dir")
    p = subprocess.Popen([sys.executable, "-u", SUITE], cwd=ROOT, env=broken,
                         stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    out = p.communicate()[0] or ""
    if p.returncode != 0:
        ok("C: the suite exits non-zero when it cannot build a sandbox (rc=%d)" % p.returncode)
    else:
        bad("C: the suite returned success without a sandbox", out[-300:])
    if "refusing to mutate the canonical checkout" in out:
        ok("C: it says why, rather than failing silently")
    else:
        bad("C: no fail-closed message", out[-300:])
    assert_untouched("C", sha0, status0)

    print("=" * 60)
    print("MUTATION_SUITE_ISOLATION: %d passed, %d failed -> %s"
          % (passed, failed, "PASS" if failed == 0 else "FAIL"))
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
