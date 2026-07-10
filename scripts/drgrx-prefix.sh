#!/bin/sh
# drgrx-prefix.sh — 把 dragonx 配置的 libRandomX 静态库做成「符号全部加 drgrx_ 前缀、
# COMDAT 已打散」的单个 .o，使其能与 stock 配置的 librandomx.a（rx/0，原名符号）
# 同时静态链入一个 ntmpool 二进制，互不串味。
#
# 照抄 NTMminer zkrx-prefix.sh 先例（那边是 stock 加前缀、dragonx 原名；pool-core
# 反过来：rx/0 已用原名直链 build-stock，故新增的 dragonx 库加前缀）。
# 三步对应三个坑（详见 _knowledge/pitfalls/cpp-双配置库-同二进制-符号前缀.md）：
#   坑1 撞名/串味：config 常量烤进各自 .o，链错 = rx/0 跑 dragonx 常量 = 挖废块 → 步骤2 全局加前缀。
#   坑2 .a 弱符号不触发 member 提取 → 步骤1 先 `ld -r --whole-archive` 合并成单 .o。
#   坑3 COMDAT 组签名 objcopy 不改名 → 跨库去重丢段 → 构造函数调 0x0 崩 → 步骤3 删 .group。
#
# 用法：  drgrx-prefix.sh <dragonx_lib.a> <out_prefixed.o>
# 环境：  NM / OBJCOPY / LD 可覆盖工具名（交叉编译用）。
# 平台：  GNU/ELF（CI ubuntu）。金锚兜底：dragonxrx SelfTest 三层锚 + rx/0 官方向量
#         同一二进制同跑 —— 任何串味当场拦下。
set -eu

DRGLIB="$1"
OUT="$2"
NM="${NM:-nm}"
OBJCOPY="${OBJCOPY:-objcopy}"
LD="${LD:-ld}"

[ -f "$DRGLIB" ] || { echo "drgrx-prefix: 找不到 dragonx 库 $DRGLIB" >&2; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# 1) 合并整库成单 .o
"$LD" -r --whole-archive "$DRGLIB" -o "$WORK/combined.o"

# 2) 全部已定义全局符号 → drgrx_ 前缀
"$NM" -g --defined-only "$WORK/combined.o" \
  | awk 'NF==3 && $3 != "" { print $3 " drgrx_" $3 }' | sort -u > "$WORK/map.txt"
NSYM="$(wc -l < "$WORK/map.txt" | tr -d ' ')"
[ "$NSYM" -ge 10 ] || { echo "drgrx-prefix: 只采到 $NSYM 个符号，异常（nm 输出格式不符？）" >&2; \
                        "$NM" -g --defined-only "$WORK/combined.o" | head -5 >&2; exit 1; }
"$OBJCOPY" --redefine-syms="$WORK/map.txt" "$WORK/combined.o" "$WORK/renamed.o"

# 3) 打散 COMDAT：删 .group 段，防止与 stock 库跨库去重
"$OBJCOPY" --remove-section='.group' "$WORK/renamed.o" "$OUT" 2>/dev/null || cp "$WORK/renamed.o" "$OUT"

# 4) 健全校验：drgrx_ C API 在、未改名的 randomx_ C API 不在（否则与 build-stock 撞名）
"$NM" -g --defined-only "$OUT" | grep -q 'drgrx_randomx_calculate_hash' \
  || { echo "drgrx-prefix: 校验失败——无 drgrx_randomx_calculate_hash" >&2; exit 1; }
if "$NM" -g --defined-only "$OUT" | awk 'NF==3 && $3 == "randomx_calculate_hash"' | grep -q .; then
  echo "drgrx-prefix: 校验失败——仍有未改名的 randomx_calculate_hash（会与 stock 库撞名）" >&2; exit 1
fi

echo "drgrx-prefix: $OUT — $NSYM 个全局符号加 drgrx_ 前缀 + COMDAT 打散（与 stock rx/0 库隔离）"
