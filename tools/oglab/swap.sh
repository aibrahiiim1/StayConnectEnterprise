#!/bin/bash
# swap.sh <bin...> : replace the named binaries from /rel/bin on the lab appliance (originals kept once as .orig)
for b in "$@"; do
  cp -n /opt/stayconnect/bin/$b /opt/stayconnect/bin/$b.orig 2>/dev/null
  systemctl stop stayconnect-$b 2>&1 | tail -1
  install -m 0755 /rel/bin/$b /opt/stayconnect/bin/$b
  systemctl reset-failed stayconnect-$b 2>/dev/null
  systemctl start --no-block stayconnect-$b
done
sleep 6
for b in "$@"; do echo -n "$b: "; systemctl is-active stayconnect-$b; done
