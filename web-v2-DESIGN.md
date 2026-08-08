# Design — NTMminer Pools（www.ntmminer.com）

锁定的设计系统（2026-08-08 redesign，hallmark · Cobalt）。改任何页面前先读本文件；
要偏离先改本文件，不做页面级私改。

## Genre
modern-minimal（开发者工具/仪表盘学派：冷静、精确、工程感）

## 结构族（macrostructure family）
- 首页：Stat-led product home —— 左字右标识 hero + 实时数据卡 + 币卡目录
- 币页：Workbench —— 工具优先，sticky tab 驱动，数据面板
- 下载页：Dev-tool / CLI —— 命令为主角，石墨代码卡
- DOM 账户页：Long-form 指南（沿用既有 DOM 结构，仅换 token）

## Theme — Cobalt（冷白 + 唯一钴蓝信号）
CSS token 名沿用旧站（`--bg-canvas` 等），**只换值**（notice.css 等旧消费者自动跟随）。
hex 均由 OKLCH 设计值换算（兼容旧浏览器，不写 oklch()）：

- `--bg-canvas`   #f8fafc（冷调工程白，非纯白）
- `--bg-sunken`   #f1f5f9
- `--surface-1`   #ffffff（卡面）
- `--surface-2`   #f1f5f9 / `--surface-3` #e8eef5 / `--surface-hover` #eef2f7
- `--line-subtle` #e2e8f0 / `--line-strong` #cbd5e1（hairline 是主要造型手段）
- `--text-primary` #0f172a / `--text-secondary` #334155 / `--text-muted` #64748b
- `--brand-cyan`（=信号色 accent）#2563eb · hover #1d4ed8 —— **每屏 <5%，只用于：
  主按钮、激活态下划线、链接、focus 环、micro-label 刻度**
- `--graphite` #182031（命令框专用暗面 —— 全站唯一深色节拍）
- 语义：success #15803d · warning #b45309 · critical #be123c · error #b91c1c

## Typography
- Display：Space Grotesk 500/700（自托管变量字体，latin；CJK 回退系统黑体）——标题、大数字
- Body：Inter 400/600（自托管变量字体，latin）+ 系统 CJK
- Mono：JetBrains Mono（自托管变量字体）——命令、地址、hash、表头、micro-label、数据值
- micro-label（旧 .section-kicker/.eyebrow）：JBM 11px UPPERCASE +.08em 字距，muted 色 + 2px 钴蓝刻度块
- 数字一律 `font-variant-numeric: tabular-nums`

## 组件语言
- 半径：按钮/输入/chip 6px，卡 10-12px（"尺子画出来的"，不用胶囊 CTA）
- 卡：白面 + hairline 边，阴影至多 0 1px 2px；hover 换边色不加 glow
- 主按钮：实心钴蓝 6px；次按钮：白底 hairline；禁用一切渐变
- 状态徽章：浅色 tint 底 + 深色字 + 同族 hairline 边，6px 矩形 chip
- 命令框：石墨 #182031 暗卡，头部=功能性 label + 复制按钮（不画假窗口点）
- 表格：thead mono uppercase 11px；行 hairline 分隔；≤767px 塌缩为卡片（沿用旧机制）

## 图表（dataviz palette，validated）
- 网格 hairline #e2e8f0，轴字 muted，tooltip 白底 hairline
- 块状态色（CSS 属性选择器覆盖 JS 字面量，JS 不动；validate_palette.js 实跑过）：
  confirmed #3DDC97→#0ca30c · pending #FFCC66→#eda100（2.17:1，relief=墨环+tooltip+表格视图）·
  orphaned #FF5C73→#d03b3b **且渲染为空心红环**（形状通道，破红绿 CVD 混淆）·
  unknown #B8D6E6→#64748b（灰=语义故意）· solo #8072FA→#4a3aa7
  残留 flag（红绿 CVD 4.1 / unknown 低饱和）与 dataviz 参考 status palette 自身同源，
  按其规则以 icon+label+形状补契，不裸靠颜色
- 币色曲线对浅底不足 3:1 的逐币覆盖：juno #d9a62e→#a17410 · dom #22c1a4→#0d8a72 ·
  btc09 #f7931a→#c26d05 · noctari #8b7cf6→#6350e6（brisvia/dragonx/velkar 保持原值）

## Motion
- 无 scroll-reveal（SPA 20s 自刷新会闪）；hover 只变边色/下划线；`prefers-reduced-motion` 全静
- easing 只用 cubic-bezier(0.16,1,0.3,1)，120-220ms

## 移动端硬指标（每次发版都验）
320/375/414/768 四宽零横向滚动；html/body `overflow-x: clip`；
hero-actions 可换行；显示级标题 `overflow-wrap: anywhere`；
图格 `minmax(0,1fr)`；点击目标 ≥44px

## 页面必须共享
lockup/mark、钴蓝信号色及其克制用法、三件字体、按钮语言、micro-label 语言

## 页面可以不同
币页 tab 内面板组合；下载页石墨卡密度；DOM 指南的长文排版

## 部署铁律（沿用）
改前先 scp 拉生产基线；js/css 改内容必换 release 名并同步 index.html；
config.js/index.html 原名改（no-cache）；上线后公网 sha256 对拍 + 无头 Chrome 真站渲染
