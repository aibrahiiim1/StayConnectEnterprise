#!/usr/bin/env python3
"""
generate-change-manifest.py — deterministic changed-file manifest for a Phase/milestone delivery.

Required by docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md §4: every final report must embed the
VERBATIM output of this tool. It derives the complete set of affected files from the Phase base
commit to HEAD using Git only (no hand-written lists), classifies each file, and prints total
diff statistics, the working-tree status, and the commit list.

Usage:
  python tools/generate-change-manifest.py                 # base = merge-base(default-branch, HEAD)
  python tools/generate-change-manifest.py <base>          # head defaults to HEAD
  python tools/generate-change-manifest.py <base>..HEAD    # explicit range
  python tools/generate-change-manifest.py <base> <head>   # explicit endpoints
  python tools/generate-change-manifest.py <base> --verified path1 path2   # add UNCHANGED-BUT-VERIFIED rows
  python tools/generate-change-manifest.py <base> --out manifest.md        # also write to a file

Deterministic: rows are sorted by path; no timestamps, no randomness. Same repo + same range =>
byte-identical output.
"""
import subprocess, sys, os, re

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

def git(*a):
    r = subprocess.run(["git", "-C", ROOT, *a], capture_output=True, text=True)
    return r.stdout

def git_ok(*a):
    r = subprocess.run(["git", "-C", ROOT, *a], capture_output=True, text=True)
    return r.returncode == 0, r.stdout.strip()

def resolve_range(argv):
    """Return (base_sha, head_sha, verified[], out_path, staged, head_label). Accepts the usage forms above.

    --staged (alias --worktree): describe base..<staged index> instead of base..<committed head>. Used by the
    manifest self-reference protocol (docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md §4.1): the delivery-only commit's
    complete path set is captured from `git add -A` staged content, and the manifest records --head-label
    (inventory_head) as its provenance HEAD while enumerating the entire base..delivery_head diff.
    """
    verified, out, staged, head_label = [], None, False, None
    pos = []
    i = 0
    while i < len(argv):
        a = argv[i]
        if a == "--verified":
            i += 1
            while i < len(argv) and not argv[i].startswith("--"):
                verified.append(argv[i]); i += 1
            continue
        if a == "--out":
            out = argv[i+1] if i+1 < len(argv) else None; i += 2; continue
        if a in ("--staged", "--worktree"):
            staged = True; i += 1; continue
        if a == "--head-label":
            head_label = argv[i+1] if i+1 < len(argv) else None; i += 2; continue
        pos.append(a); i += 1

    base = head = None
    if pos and ".." in pos[0]:
        base, _, head = pos[0].partition("..")
        head = head or "HEAD"
    elif len(pos) >= 2:
        base, head = pos[0], pos[1]
    elif len(pos) == 1:
        base, head = pos[0], "HEAD"
    else:
        # default base: merge-base of the default branch with HEAD
        head = "HEAD"
        for ref in ("origin/main", "main", "origin/master", "master"):
            ok, mb = git_ok("merge-base", ref, "HEAD")
            if ok and mb:
                base = mb; break
        if not base:
            base = git("rev-list", "--max-parents=0", "HEAD").split("\n")[0].strip()  # root commit
    ok_b, base_sha = git_ok("rev-parse", base)
    ok_h, head_sha = git_ok("rev-parse", head)
    if not ok_b: sys.exit(f"ERROR: cannot resolve base '{base}'")
    if not ok_h: sys.exit(f"ERROR: cannot resolve head '{head}'")
    return base_sha, head_sha, verified, out, staged, head_label

STATUS_WORD = {"A": "CREATED", "M": "MODIFIED", "D": "DELETED", "R": "RENAMED",
               "C": "COPIED", "T": "MODIFIED"}  # T = type change treated as modified

def domain_of(path):
    p = path.replace("\\", "/")
    if p.startswith("exports/"): return "export"
    if p.startswith("governance/"): return "governance"
    if "/migrations/" in p or p.endswith((".up.sql", ".down.sql")): return "database"
    if p.startswith("tools/") or "/tests/" in p or p.endswith("_test.go"): return "tests/tooling"
    if p.startswith("docs/"): return "documentation"
    if p.startswith("deploy/") or p.endswith((".yml", ".yaml", ".conf", ".env.example", ".service")): return "configuration"
    if p.startswith(("data-plane/", "control-plane/", "cloud-admin/", "hotel-admin/", "web-admin/")): return "runtime"
    return "other"

# in-repo pure-generated (rendered by tooling), outside exports/
GENERATED_BASENAMES = {"00-START-HERE.md", "PROJECT-INSTRUCTIONS.md", "MANIFEST.md",
                       "PACK_SHA256SUMS.txt"}

def classify(status_letter, path):
    word = STATUS_WORD.get(status_letter[0], status_letter)
    p = path.replace("\\", "/")
    if p.startswith("exports/"):
        return "EXPORTED"
    if os.path.basename(p) in GENERATED_BASENAMES:
        return "GENERATED"
    return word

def rollback_of(word):
    if word == "CREATED": return "rollback REMOVES it"
    if word == "DELETED": return "rollback RESTORES it"
    return "rollback RESTORES prior content"

def purpose_of(path, base, head):
    """Deterministic best-effort purpose = subject of the most recent commit in range touching the file."""
    out = git("log", "--format=%s", "-1", f"{base}..{head}", "--", path).strip()
    return out.splitlines()[0] if out else "(no commit subject in range)"

def workstream_of(path, subject):
    """Reproducible owning workstream / migration group. Prefer an explicit bracket prefix
    on the latest touching commit subject (e.g. [W0], [MG-3], [GOVERNANCE], [EXPORT]); otherwise
    fall back to a deterministic path-based classification. Never empty."""
    m = re.match(r"\s*\[([^\]]+)\]", subject or "")
    if m:
        tag = m.group(1).strip().upper()
        if tag: return tag
    p = path.replace("\\", "/")
    if p.startswith(".github/"): return "CI"
    if p.startswith("exports/"): return "EXPORT"
    if p.startswith("governance/"): return "GOVERNANCE"
    if "/migrations/" in p or p.endswith((".up.sql", ".down.sql")): return "MIGRATIONS"
    if p.startswith("tools/"): return "TOOLING"
    if p.startswith("docs/"): return "DOCS"
    if p.startswith("deploy/"): return "DEPLOY"
    if p.startswith(("data-plane/", "control-plane/", "cloud-admin/", "hotel-admin/", "web-admin/")): return "RUNTIME"
    return "OTHER"

def main():
    base, head, verified, out_path, staged, head_label = resolve_range(sys.argv[1:])
    branch = git("rev-parse", "--abbrev-ref", "HEAD").strip()
    remote_branch = git("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}").strip() or "(no upstream)"
    # In staged mode the manifest describes base..<staged index> (the exact content of the next commit =
    # delivery_head) and records --head-label (inventory_head) as its provenance HEAD. Otherwise it
    # describes the committed base..head range.
    head_display = (head_label or "STAGED-INDEX (delivery_head)") if staged else head
    diff_args = (["--cached", f"{base}"] if staged else [f"{base}..{head}"])

    # name-status with rename/copy detection
    ns = git("diff", "--name-status", "--find-renames", "--find-copies", *diff_args)
    rows = []  # (path_display, classification, git_status, domain, workstream, rollback, purpose)
    for line in ns.splitlines():
        if not line.strip(): continue
        parts = line.split("\t")
        st = parts[0]
        if st[0] in ("R", "C") and len(parts) >= 3:
            old, new = parts[1], parts[2]
            disp = f"{old} -> {new}"
            word = classify(st, new)
            git_st = f"{st} ({old} -> {new})"
            purp = purpose_of(new, base, head)
            rows.append((disp, word, git_st, domain_of(new), workstream_of(new, purp), rollback_of(STATUS_WORD.get(st[0], st)), purp))
        else:
            path = parts[-1]
            word = classify(st, path)
            purp = purpose_of(path, base, head)
            rows.append((path, word, st, domain_of(path), workstream_of(path, purp), rollback_of(STATUS_WORD.get(st[0], st)), purp))
    rows.sort(key=lambda r: r[0])

    stat = git("diff", "--stat", *diff_args).rstrip()
    wt = git("status", "--short", "--untracked-files=all").rstrip()
    commits = git("log", "--oneline", f"{base}..{head}").rstrip()
    submods = git("submodule", "status").rstrip()

    L = []
    L.append("# Changed-file manifest (generated - do not hand-edit)")
    L.append("")
    L.append(f"- **Base commit:** `{base}`")
    L.append(f"- **HEAD commit:** `{head_display}`")
    if staged:
        L.append(f"- **Provenance (generation HEAD = inventory_head):** `{head}`  ·  path/status set covers the complete `base..delivery_head` diff (delivery_head = this staged content once committed).")
    L.append(f"- **Branch:** `{branch}`")
    L.append(f"- **Remote branch:** `{remote_branch}`")
    L.append(f"- **Changed files:** {len(rows)}")
    L.append(f"- **Generated by:** `tools/generate-change-manifest.py {base[:12]}..{('STAGED' if staged else head[:12])}`")
    L.append("")
    L.append("## Files")
    L.append("")
    L.append("| Path | Classification | Git status | Domain | Workstream | Rollback | Purpose (last commit subject in range) |")
    L.append("|---|---|---|---|---|---|---|")
    for disp, word, git_st, dom, ws, rb, purp in rows:
        purp = purp.replace("|", "\\|")
        L.append(f"| `{disp}` | {word} | `{git_st}` | {dom} | {ws} | {rb} | {purp} |")
    if not rows:
        L.append("| *(no changes in range)* | | | | | | |")
    L.append("")

    if verified:
        L.append("## UNCHANGED-BUT-VERIFIED")
        L.append("")
        L.append("| Path | Classification | Domain | Workstream |")
        L.append("|---|---|---|---|")
        for p in sorted(verified):
            L.append(f"| `{p}` | UNCHANGED-BUT-VERIFIED | {domain_of(p)} | {workstream_of(p, '')} |")
        L.append("")

    L.append("## Total diff statistics (`git diff --stat`)")
    L.append("```text")
    L.append(stat if stat else "(no diff)")
    L.append("```")
    L.append("")
    L.append("## Working-tree status (`git status --short --untracked-files=all`)")
    L.append("```text")
    L.append(wt if wt else "(clean)")
    L.append("```")
    L.append("")
    L.append("## Commits in range (`git log --oneline <base>..HEAD`)")
    L.append("```text")
    L.append(commits if commits else "(none)")
    L.append("```")
    if submods.strip():
        L.append("")
        L.append("## Submodule status")
        L.append("```text")
        L.append(submods)
        L.append("```")
    text = "\n".join(L) + "\n"

    sys.stdout.write(text)
    if out_path:
        with open(out_path, "w", encoding="utf-8", newline="\n") as f:
            f.write(text)
        sys.stderr.write(f"[written] {out_path}\n")

if __name__ == "__main__":
    main()
