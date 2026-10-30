#!/bin/sh
# Runs INSIDE a plain ubuntu:22.04 container: installs the base set with retries (mirror flakiness), then masks container-hostile units.
export DEBIAN_FRONTEND=noninteractive
mkdir -p /etc/ssl/certs && cp /ca-bundle.crt /etc/ssl/certs/ca-certificates.crt
echo 'Acquire::https::CaInfo "/etc/ssl/certs/ca-certificates.crt"; Acquire::Retries "5"; Acquire::http::Timeout "25"; Acquire::Queue-Mode "access"; Acquire::http::Pipeline-Depth "0";' > /etc/apt/apt.conf.d/80retry
n=0
while [ $n -lt 25 ]; do
  n=$((n+1))
  for m in https://mirror.leaseweb.com https://mirrors.edge.kernel.org; do
    printf "deb $m/ubuntu jammy main universe
deb $m/ubuntu jammy-updates main universe
deb $m/ubuntu jammy-security main universe
" > /etc/apt/sources.list
    apt-get update -qq >/dev/null 2>&1 || continue
    if apt-get install -y -qq --no-install-recommends \
      systemd systemd-sysv dbus iproute2 iputils-ping nftables conntrack ethtool openssh-server sudo curl wget gnupg \
      ca-certificates lsb-release python3 jq git less procps psmisc kmod tzdata cron netcat-openbsd dnsutils tcpdump \
      unzip xz-utils bridge-utils iptables lsof vim-tiny file openssl isc-dhcp-client >/tmp/apt.out 2>&1; then
      echo "INSTALLED via $m on attempt $n"
      # the lab mirror stays in place so the installers' own apt calls work too
      systemctl mask getty@tty1.service getty.target console-getty.service systemd-udevd.service systemd-udev-trigger.service \
        systemd-modules-load.service sys-kernel-config.mount sys-kernel-debug.mount sys-kernel-tracing.mount systemd-remount-fs.service \
        systemd-resolved.service apt-daily.timer apt-daily-upgrade.timer motd-news.timer e2scrub_all.timer fstrim.timer \
        systemd-timesyncd.service multipathd.service >/dev/null 2>&1 || true
      mkdir -p /root/.ssh && chmod 700 /root/.ssh && systemctl enable ssh >/dev/null 2>&1 || true
      apt-get clean; rm -rf /var/lib/apt/lists/*
      echo BAKE_OK; exit 0
    fi
    echo "attempt $n via $m failed: $(grep -E '^E:' /tmp/apt.out | head -1 | cut -c1-100)"
  done
done
echo BAKE_FAILED; exit 1
