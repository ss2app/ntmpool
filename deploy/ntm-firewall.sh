#!/bin/bash
# ntm-firewall.sh — 103.80.18.140 主机防火墙（幂等，可重复执行）
# 设计（2026-07-12，DDoS 事件后加固）：
#   INPUT 默认丢弃，白名单放行：
#     - lo / 已建立连接 / ICMP / docker 网桥 / 机房内网 10.0.12.0/24
#     - SSH 8422 全网开（key-only；web161/前端/hk2 隧道都走这里）
#     - stratum 端口只认两台中转机：
#         209 (103.149.200.209): 5333,5444,15333,13333,17011,18344,18345
#         drgx 中转 (122.10.119.40): 5333,5444
#   DOCKER-USER 追加：旧 midstate fork 池 web :8000 只认 209/161。
#   节点 P2P (9009/9010/9333/21768/28080) 保持公网开 —— 链同步/爆块传播需要。
#   已有的 3333/8344 源锁服务规则不动，与本脚本共存。
set -u

FWD209=103.149.200.209
DRGXFWD=122.10.119.40
WEB161=161.33.163.182
STRATUM_209="5333,5444,15333,13333,17011,18344,18345"
STRATUM_DRGX="5333,5444"

# ---------- INPUT（主机服务） ----------
iptables -N NTM-FW 2>/dev/null || iptables -F NTM-FW

iptables -A NTM-FW -i lo -j ACCEPT
iptables -A NTM-FW -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -A NTM-FW -p icmp -j ACCEPT
# docker 网桥/容器 → 主机（内部流量）
iptables -A NTM-FW -s 172.16.0.0/12 -j ACCEPT
# 机房内网（网关/监控）
iptables -A NTM-FW -s 10.0.12.0/24 -j ACCEPT
# SSH（key-only）
iptables -A NTM-FW -p tcp --dport 8422 -j ACCEPT
# stratum 只认中转机
iptables -A NTM-FW -s $FWD209  -p tcp -m multiport --dports $STRATUM_209  -j ACCEPT
iptables -A NTM-FW -s $DRGXFWD -p tcp -m multiport --dports $STRATUM_DRGX -j ACCEPT
# 其余一律丢
iptables -A NTM-FW -j DROP

# 挂到 INPUT 最前（幂等）
iptables -C INPUT -j NTM-FW 2>/dev/null || iptables -I INPUT 1 -j NTM-FW

# ---------- IPv6：只留 lo/已建立/ICMPv6/SSH ----------
if command -v ip6tables >/dev/null 2>&1; then
  ip6tables -N NTM-FW 2>/dev/null || ip6tables -F NTM-FW
  ip6tables -A NTM-FW -i lo -j ACCEPT
  ip6tables -A NTM-FW -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
  ip6tables -A NTM-FW -p ipv6-icmp -j ACCEPT
  ip6tables -A NTM-FW -p tcp --dport 8422 -j ACCEPT
  ip6tables -A NTM-FW -j DROP
  ip6tables -C INPUT -j NTM-FW 2>/dev/null || ip6tables -I INPUT 1 -j NTM-FW
fi

# ---------- DOCKER-USER（容器映射端口） ----------
# 保证链存在（docker 未起时也不报错；docker 起来会直接使用既有链）
iptables -N DOCKER-USER 2>/dev/null || true
# 旧 midstate fork 池 web :8000 —— 只认 209/161，其余丢（P2P 端口不动，保持公网）
iptables -C DOCKER-USER -s $FWD209 -p tcp --dport 8000 -m comment --comment ntm-fw-8000 -j RETURN 2>/dev/null || \
  iptables -I DOCKER-USER 1 -s $FWD209 -p tcp --dport 8000 -m comment --comment ntm-fw-8000 -j RETURN
iptables -C DOCKER-USER -s $WEB161 -p tcp --dport 8000 -m comment --comment ntm-fw-8000 -j RETURN 2>/dev/null || \
  iptables -I DOCKER-USER 2 -s $WEB161 -p tcp --dport 8000 -m comment --comment ntm-fw-8000 -j RETURN
iptables -C DOCKER-USER -p tcp --dport 8000 -m comment --comment ntm-fw-8000 -j DROP 2>/dev/null || \
  iptables -I DOCKER-USER 3 -p tcp --dport 8000 -m comment --comment ntm-fw-8000 -j DROP

echo "ntm-firewall applied: $(date '+%F %T')"
iptables -S NTM-FW | sed 's/^/  /'
