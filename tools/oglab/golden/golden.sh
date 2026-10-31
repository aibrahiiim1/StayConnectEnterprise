#!/usr/bin/env bash
# The golden OneGate lab: build the lab ONCE by the product's own installers, freeze it into versioned images and volume snapshots,
# and from then on reset it in about a minute instead of rebuilding it (2026-10-31; the last mission spent hours rebuilding).
# Runs on the Docker host. TEST tooling only: nothing here is installed on an appliance, nothing reaches the production Central.
#
#   golden.sh freeze <tag>     stop the running lab cleanly and record it: images (docker commit), every volume (tar), manifest
#   golden.sh reset  <tag>     throw the lab away and recreate it from <tag> (containers + volumes), then check
#   golden.sh check  <tag>     the running lab is <tag>: release SHA read from the installed binary, schema version, fixture hash
#   golden.sh upgrade <dir>    install the release under test on the reset lab by the product's own upgrade path (never a downgrade)
#   golden.sh run <scenario>   one accelerated scenario, hard limit 300 s real time (scenarios/), result in results/
#   golden.sh list             golden tags on this host
#   golden.sh fresh            the separate fresh-install verification path (README "Fresh install"); never replaced by reset
#
# Status 2026-10-31: PREPARED, NOT EXECUTED (test execution was suspended by the Product Owner). Every step prints what it does.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="${OGLAB_GOLDEN_ROOT:-$HOME/oglab-golden}"
CONTAINERS=(${OGLAB_CONTAINERS:-oglab-central oglab-appl oglab-gen oglab-sink oglab-sftp})
APPL="${OGLAB_APPLIANCE:-oglab-appl}"
LIMIT="${OGLAB_SCENARIO_LIMIT:-300}"

say() { printf '%s golden: %s\n' "$(date -u +%H:%M:%S)" "$*"; }
die() { say "ERROR: $*"; exit 1; }
need() { command -v "$1" >/dev/null || die "$1 is required"; }
need docker; need jq; need sha256sum

exists() { docker inspect "$1" >/dev/null 2>&1; }

# The appliance's own provenance: the release is what is installed, read back from the binary, never what the lab was told.
appliance_release() {
  docker exec "$APPL" sh -c 'go version -m /opt/stayconnect/bin/scd 2>/dev/null | awk "/vcs.revision/{print \$3}"' 2>/dev/null \
    || docker exec "$APPL" sh -c 'cat /opt/stayconnect/RELEASE 2>/dev/null' || true
}
appliance_schema() {
  docker exec "$APPL" sh -c "docker exec stayconnect-pg psql -X -At -U stayconnect -d stayconnect_site -c \
    'SELECT max(version) FROM public.schema_migrations'" 2>/dev/null | tr -d '[:space:]' || true
}
fixture_hash() {
  # The fixture is what scenarios rely on: networks, policy, vouchers, the generator's client list. Hash of their canonical dump.
  docker exec "$APPL" sh -c "docker exec stayconnect-pg psql -X -At -U stayconnect -d stayconnect_site -c \
    \"SELECT md5(string_agg(x, '|' ORDER BY x)) FROM (
        SELECT 'net:'||id||':'||coalesce(cidr::text,'') AS x FROM public.guest_networks
        UNION ALL SELECT 'pkg:'||id||':'||coalesce(current_revision_id::text,'') FROM iam_v2.internet_packages
        UNION ALL SELECT 'vou:'||count(*)::text FROM iam_v2.vouchers) f\"" 2>/dev/null | tr -d '[:space:]' || true
}

cmd_freeze() {
  local tag="${1:?tag}" dir="$ROOT/$1"
  [ -e "$dir" ] && die "$dir exists; a golden tag is immutable (choose a new tag)"
  mkdir -p "$dir/volumes"
  local rel schema fix
  rel="$(appliance_release)"; schema="$(appliance_schema)"; fix="$(fixture_hash)"
  [ -n "$rel" ] && [ -n "$schema" ] || die "the appliance's release or schema version could not be read: the lab is not in a known state"
  say "freezing $tag (release $rel, schema $schema, fixture $fix)"
  local specs="[]"
  for c in "${CONTAINERS[@]}"; do
    exists "$c" || { say "skip $c (not present)"; continue; }
    # A clean systemd shutdown inside the container, so PostgreSQL and the services stop as they would on a power-off button.
    say "stopping $c cleanly"; docker stop -t 120 "$c" >/dev/null
    docker inspect "$c" > "$dir/$c.inspect.json"
    say "committing $c"; docker commit "$c" "oglab/golden-$c:$tag" >/dev/null
    local mounts; mounts="$(jq -r '.[0].Mounts[] | select(.Type=="volume") | .Destination' "$dir/$c.inspect.json")"
    if [ -n "$mounts" ]; then
      # shellcheck disable=SC2086
      docker run --rm --volumes-from "$c" -v "$dir/volumes:/snap" alpine:3.20 sh -c \
        "tar -C / -cf /snap/$c.tar $(echo $mounts | sed 's#^/##; s# /# #g')" \
        || die "the volumes of $c could not be saved"
      sha256sum "$dir/volumes/$c.tar" | awk '{print $1}' > "$dir/volumes/$c.tar.sha256"
    fi
    specs="$(jq --arg c "$c" --arg img "oglab/golden-$c:$tag" '. + [{container:$c, image:$img}]' <<<"$specs")"
  done
  jq -n --arg tag "$tag" --arg rel "$rel" --arg schema "$schema" --arg fix "$fix" --arg at "$(date -u +%FT%TZ)" \
     --argjson containers "$specs" \
     '{tag:$tag, release_sha:$rel, schema_version:$schema, fixture_hash:$fix, frozen_at:$at, containers:$containers}' > "$dir/manifest.json"
  say "frozen: $dir/manifest.json"
  for c in "${CONTAINERS[@]}"; do exists "$c" && docker start "$c" >/dev/null || true; done
}

# Recreate a container from its recorded docker inspect: the same image (the golden one), networks, mounts, limits and privileges.
recreate() {
  local c="$1" dir="$2" spec="$2/$1.inspect.json" img
  img="$(jq -r --arg c "$c" '.containers[] | select(.container==$c) | .image' "$dir/manifest.json")"
  local args=(--name "$c" --hostname "$(jq -r '.[0].Config.Hostname' "$spec")")
  [ "$(jq -r '.[0].HostConfig.Privileged' "$spec")" = "true" ] && args+=(--privileged)
  [ "$(jq -r '.[0].HostConfig.CgroupnsMode' "$spec")" = "private" ] && args+=(--cgroupns private)
  local cpus mem cpuset
  cpus="$(jq -r '.[0].HostConfig.NanoCpus' "$spec")"; mem="$(jq -r '.[0].HostConfig.Memory' "$spec")"
  cpuset="$(jq -r '.[0].HostConfig.CpusetCpus' "$spec")"
  [ "$cpus" != "0" ] && args+=(--cpus "$(awk -v n="$cpus" 'BEGIN{print n/1e9}')")
  [ "$mem" != "0" ] && args+=(--memory "$mem")
  [ -n "$cpuset" ] && args+=(--cpuset-cpus "$cpuset")
  while read -r m; do [ -n "$m" ] && args+=(-v "$m"); done < <(jq -r '.[0].Mounts[] | if .Type=="volume" then .Destination else .Source+":"+.Destination end' "$spec")
  while read -r t; do [ -n "$t" ] && args+=(--tmpfs "$t"); done < <(jq -r '.[0].HostConfig.Tmpfs // {} | keys[]' "$spec")
  local net; net="$(jq -r '.[0].HostConfig.NetworkMode' "$spec")"
  args+=(--network "$net")
  local ip; ip="$(jq -r --arg n "$net" '.[0].NetworkSettings.Networks[$n].IPAMConfig.IPv4Address // empty' "$spec")"
  [ -n "$ip" ] && args+=(--ip "$ip")
  docker create "${args[@]}" "$img" >/dev/null
  # Further networks the container was attached to (the WAN side, the LAN side).
  while read -r n; do
    [ "$n" = "$net" ] && continue
    local nip; nip="$(jq -r --arg n "$n" '.[0].NetworkSettings.Networks[$n].IPAMConfig.IPv4Address // empty' "$spec")"
    if [ -n "$nip" ]; then docker network connect --ip "$nip" "$n" "$c"; else docker network connect "$n" "$c"; fi
  done < <(jq -r '.[0].NetworkSettings.Networks | keys[]' "$spec")
  if [ -f "$dir/volumes/$c.tar" ]; then
    [ "$(sha256sum "$dir/volumes/$c.tar" | awk '{print $1}')" = "$(cat "$dir/volumes/$c.tar.sha256")" ] || die "the volume snapshot of $c is damaged"
    docker run --rm --volumes-from "$c" -v "$dir/volumes:/snap:ro" alpine:3.20 tar -C / -xf "/snap/$c.tar"
  fi
}

cmd_reset() {
  local tag="${1:?tag}" dir="$ROOT/$1" t0; t0=$(date +%s)
  [ -f "$dir/manifest.json" ] || die "no golden lab $tag (golden.sh list)"
  for c in "${CONTAINERS[@]}"; do exists "$c" && { say "removing $c"; docker rm -f -v "$c" >/dev/null; }; done
  for c in $(jq -r '.containers[].container' "$dir/manifest.json"); do say "recreating $c"; recreate "$c" "$dir"; done
  for c in $(jq -r '.containers[].container' "$dir/manifest.json"); do docker start "$c" >/dev/null; done
  # The appliance's services and PostgreSQL come up under systemd; wait for the database and the plan path, bounded.
  local n=0
  until [ -n "$(appliance_schema)" ] || [ $n -ge 60 ]; do sleep 2; n=$((n+1)); done
  cmd_check "$tag"
  say "reset to $tag in $(( $(date +%s) - t0 )) s"
}

cmd_check() {
  local tag="${1:?tag}" dir="$ROOT/$1"
  local want_rel want_schema want_fix rel schema fix bad=0
  want_rel="$(jq -r .release_sha "$dir/manifest.json")"; want_schema="$(jq -r .schema_version "$dir/manifest.json")"
  want_fix="$(jq -r .fixture_hash "$dir/manifest.json")"
  rel="$(appliance_release)"; schema="$(appliance_schema)"; fix="$(fixture_hash)"
  [ "$rel" = "$want_rel" ] || { say "release $rel, golden $want_rel (upgrade with golden.sh upgrade, or the lab is not $tag)"; bad=1; }
  [ "$schema" = "$want_schema" ] || { say "schema $schema, golden $want_schema"; bad=1; }
  [ "$fix" = "$want_fix" ] || { say "fixture $fix, golden $want_fix"; bad=1; }
  [ $bad -eq 0 ] && say "the lab is $tag: release $rel, schema $schema, fixture $fix" || return 1
}

cmd_upgrade() {
  local rel="${1:?release directory}"
  [ -d "$rel" ] || die "$rel is not a directory"
  local before; before="$(appliance_schema)"
  say "installing $rel on $APPL by the product's upgrade path (schema now $before)"
  docker cp "$rel" "$APPL:/rel-upgrade"
  docker exec "$APPL" bash /rel-upgrade/deploy/scripts/appliance-install.sh --upgrade || die "the upgrade failed: reset the lab before the next run"
  local after; after="$(appliance_schema)"
  [ "${after:-0}" \< "${before:-0}" ] && die "schema went from $before to $after: a golden lab is never downgraded"
  say "upgraded: release $(appliance_release), schema $after"
}

cmd_run() {
  local s="${1:?scenario}" f="$HERE/scenarios/$1.sh"
  [ -x "$f" ] || die "no scenario $s (ls $HERE/scenarios)"
  mkdir -p "$ROOT/results"
  local out="$ROOT/results/$(date -u +%Y%m%dT%H%M%SZ)-$s" t0 rc=0; t0=$(date +%s)
  say "running $s (hard limit ${LIMIT} s)"
  timeout --signal=TERM --kill-after=10 "$LIMIT" "$f" > "$out.log" 2>&1 || rc=$?
  local el=$(( $(date +%s) - t0 )) verdict
  case $rc in
    0) verdict=PASS ;;
    124|137) verdict="NOT VERIFIED (exceeded ${LIMIT} s)" ;;
    *) verdict=FAIL ;;
  esac
  jq -n --arg s "$s" --arg v "$verdict" --argjson rc "$rc" --argjson el "$el" --arg rel "$(appliance_release)" --arg at "$(date -u +%FT%TZ)" \
     '{scenario:$s, verdict:$v, exit:$rc, elapsed_s:$el, release_sha:$rel, finished_at:$at}' > "$out.json"
  say "$s: $verdict in ${el} s ($out.log)"
  [ "$verdict" = PASS ]
}

cmd_list() { for m in "$ROOT"/*/manifest.json; do [ -f "$m" ] && jq -c '{tag,release_sha,schema_version,frozen_at}' "$m"; done; }

cmd_fresh() {
  cat <<'TXT'
The fresh-install path is NOT replaced by the golden lab. Run it for a release that changes an installer, a migration chain, Gate-P or
the base image (tools/oglab/README.md "Order of use"): bake.sh, docker run, central-install.sh --mode new, appliance-install.sh, activate,
lab_networks.py / lab_policy.py / lab_setup.py. Then golden.sh freeze <new-tag> to make it the next golden lab.
TXT
}

case "${1:-}" in
  freeze) shift; cmd_freeze "$@" ;;
  reset) shift; cmd_reset "$@" ;;
  check) shift; cmd_check "$@" ;;
  upgrade) shift; cmd_upgrade "$@" ;;
  run) shift; cmd_run "$@" ;;
  list) cmd_list ;;
  fresh) cmd_fresh ;;
  *) sed -n '2,16p' "$0"; exit 2 ;;
esac
