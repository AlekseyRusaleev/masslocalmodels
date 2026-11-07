#!/usr/bin/env bash
# pve-audit.sh — READ-ONLY inventory of the Proxmox host.
#
# Собирает только сведения: ничего не создаёт, не запускает, не останавливает,
# не изменяет конфигурацию VM/CT/storage/network/firewall.
#
# Запуск с рабочей машины:
#   export PVE_HOST=... PVE_PORT=... PVE_USER=root PVE_PASS='***'
#   python tools/ssh_run.py -f scripts/pve-audit.sh
#
# Формат вывода: секции "===== NAME =====", пригоден для диффа между запусками.

set -u

section() { printf '\n===== %s =====\n' "$1"; }

section "IDENTITY"
hostname
pveversion
uptime
date -u
uname -r

section "CLUSTER"
pvecm status 2>&1 | head -20

section "CPU"
lscpu | egrep -i 'model name|^CPU\(s\)|Thread|Core|Socket|MHz'
cat /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor 2>/dev/null

section "MEMORY"
free -h
egrep 'MemTotal|MemFree|MemAvailable|SwapTotal|SwapFree' /proc/meminfo
echo -n "ksm_run=";        cat /sys/kernel/mm/ksm/run 2>/dev/null
echo -n "ksm_shared_pg=";  cat /sys/kernel/mm/ksm/pages_shared 2>/dev/null
echo -n "swappiness=";     cat /proc/sys/vm/swappiness

section "GUESTS"
qm list
pct list

section "GUEST CONFIGS"
for f in /etc/pve/qemu-server/*.conf /etc/pve/lxc/*.conf; do
  [ -e "$f" ] || continue
  echo "--- $f"
  cat "$f"
done

section "GUEST RUNTIME STATUS"
for id in $(qm list | awk 'NR>1 && $3=="running" {print $1}'); do
  echo "--- VM $id"
  timeout 20 pvesh get "/nodes/$(hostname)/qemu/$id/status/current" --output-format json 2>&1 | head -5
done
for id in $(pct list | awk 'NR>1 && $2=="running" {print $1}'); do
  echo "--- CT $id"
  timeout 20 pct exec "$id" -- sh -c 'hostname; uptime; free -h; df -h /' 2>&1 | head -12
done

section "BLOCK DEVICES"
lsblk -o NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT,MODEL,ROTA 2>/dev/null

section "FILESYSTEM USAGE"
df -hT -x tmpfs -x devtmpfs 2>/dev/null

section "LVM"
pvs 2>/dev/null
vgs 2>/dev/null
lvs -o lv_name,vg_name,lv_size,data_percent,metadata_percent 2>/dev/null

section "ZFS"
zpool list 2>/dev/null
zpool status -v 2>/dev/null
zfs list -o name,used,avail,refer,volsize,refreservation 2>/dev/null
zfs list -t snapshot 2>/dev/null

section "STORAGE CONFIG"
cat /etc/pve/storage.cfg 2>/dev/null
timeout 25 pvesm status 2>&1

section "SMART"
for d in /dev/sd?; do
  echo "--- $d"
  smartctl -H -i "$d" 2>&1 | egrep -i 'model|serial|SMART overall|capacity|rotation|power on'
done

section "GPU"
lspci -nn | egrep -i 'vga|3d|display|nvidia'
(nvidia-smi 2>&1 | head -8) || true

section "MEMORY SLOTS"
dmidecode -t memory 2>/dev/null | egrep -i 'Locator|Size|Type:|Speed' | head -40

section "NETWORK"
cat /etc/network/interfaces
ip -br addr
ip route
for i in $(ls /sys/class/net); do
  [ "$i" = "lo" ] && continue
  echo -n "$i speed: "; ethtool "$i" 2>/dev/null | grep -i speed || echo n/a
done
ip -s link show vmbr0 2>/dev/null | head -8
curl -s -m 8 https://ifconfig.me; echo
curl -o /dev/null -s -m 40 -w 'download_speed_Bps=%{speed_download}\n' \
  'https://speed.cloudflare.com/__down?bytes=50000000' 2>&1

section "LISTENING PORTS"
ss -tulpn | head -40

section "FIREWALL"
pve-firewall status 2>&1 | head -5
cat /etc/pve/firewall/cluster.fw 2>/dev/null
cat "/etc/pve/nodes/$(hostname)/host.fw" 2>/dev/null
nft list tables 2>/dev/null
fail2ban-client status 2>&1 | head -6

section "TAILSCALE"
tailscale status 2>&1 | head -10

section "SERVICES"
systemctl list-units --type=service --state=running --no-pager --no-legend | awk '{print $1}'

section "TOOLING"
for c in go docker git python3 curl jq iperf3; do printf '%-8s ' "$c"; command -v "$c" || echo '-'; done

section "BACKUP JOBS"
cat /etc/pve/jobs.cfg 2>/dev/null || echo "no jobs.cfg"

section "PENDING UPDATES"
apt-get -s upgrade 2>/dev/null | egrep -c '^Inst' || true

section "TEMPLATES"
ls -la /var/lib/vz/template/iso/ 2>/dev/null
ls -la /var/lib/vz/template/cache/ 2>/dev/null

section "RECENT LOGINS"
last -n 8
