# Design v3 — NTMminer Pools（www.ntmminer.com）重设计（暖调浅色）

锁定的设计系统（2026-08-30，hallmark `study` ×2 → `redesign` 多页流程）。
DNA 来源：**hi.ht.mk**（表面材质与色调）+ **pool.edgexnetwork.org**（仪表盘信息架构与图表语言）。
取其骨架与材质，不抄像素；两站的 AI 味元素（紫粉渐变、渐变文字、发光、悬停上浮、假终端窗口点）全部不带过来。
**⚠ 2026-08-30 用户否决暗色（"不好看、看着不舒服"）→ 整套系统翻译到暖调浅色**：hi.ht.mk 的"暖 + 单一材质分层 + 单一珊瑚强调"保留，底改为暖米白。
原型：`proto/dist/ntmminer-pools-v3.html`（数据 = 2026-08-30 真实 API 快照）；页脚有「强调色试穿」余烬 / 钴蓝二选一开关（只在原型）。

## Genre
modern-minimal（暖调）：浅色暖米白画布 + 两团极低彩度光晕 + 单一暖色强调；
分层靠**亮度**（页面底 < 卡面白），hairline 造型，阴影至多 `0 1px 2px`；backdrop-blur 只用在 sticky 头部。
全站唯一深色节拍 = 命令框（石墨），命令/脚本在深底上更易读、也把"可复制的东西"从阅读内容里区分出来。

## 结构族（macrostructure family）
- 首页：**Workbench / 控制台** —— 全站状态条 → 矿池「账本行」列表（每池一行：身份 · 24h 迷你曲线 · 池算力 · 矿工/矿机 · 费率 · 高度 · 爆块）→ 三步开挖（有序步骤 + 真实命令）→ 抽水公示。不再有宣传式 hero 与 3 等分卖点卡。
- 币页：**Dashboard**（edgex 骨架）—— 页头（身份 + 外链）→ 分段式 tab 子导航 → 状态条 → 4 格 stat band（1 个 hero 数字）→ 12 栏 bento（图表 8 栏 + 小指标 4 栏 / 表格 7 栏 + 时间线 5 栏）→ 币种介绍。
- 下载页：**Dev-tool 列表** —— 锄头按行陈列（版本 · 抽水徽章 · 双平台下载 + sha256）+ 按币找锄头表。
- 页面共享：头部币种切换器（真正的一级导航 = 选哪个池）、地址/高度搜索、下载 CTA、inline 单行页脚。

## Theme — Ember on warm paper（暖米白 + 余烬红）
全部 OKLCH 设计值，hex 为换算结果（兼容旧浏览器只写 hex）：

| token | oklch | hex | 用途 |
|---|---|---|---|
| `--paper` | 97% .008 60 | `#faf5f0` | 页面底（暖米白，不是纯白） |
| `--paper-2` | 99.5% .003 70 | `#fffdfb` | 卡面 / 图表面（比页面底更亮 = 抬升） |
| `--paper-3` | 95% .009 60 | `#f3ede9` | 输入框、chips 容器、hover |
| `--paper-4` | 92.5% .010 60 | `#ebe5e0` | 更深一层 |
| `--rule` | 90% .009 60 | `#e3ddd8` | hairline（卡边、网格线） |
| `--rule-2` | 82% .010 60 | `#c9c3be` | 强边线、坐标轴、次按钮边 |
| `--ink` | 22% .012 40 | `#201916` | 主文字（16:1） |
| `--ink-2` | 42% .010 40 | `#524b49` | 次文字（8.4:1） |
| `--ink-3` | 53% .010 40 | `#716a67` | 弱文字 / micro-label（≥4.5:1 on paper） |
| `--accent` | — | `#2563eb` **钴蓝（2026-08-30 用户拍板，余烬红 `#c83b2c` 方案作废）** | **唯一 UI 强调色**：主按钮、激活态、链接、focus 环（5.1:1，白字 5.2:1） |
| `--accent-hi` | — | `#1d4ed8` | 强调 hover / focus |
| 命令框 | 20% .012 30 | 底 `#1b1413` · 字 `#f2edeb` · 地址高亮 `#8fb4ff` | 全站唯一深色节拍 |
| 光晕 | — | 钴蓝 α.09 左上 / 金 α.08 右下（暖底配一冷一暖） | 固定、不动画、两团封顶 |

强调色纪律：每屏 ≤ 5%。不做渐变、不做渐变文字、不做发光、不做悬停上浮。

## 图表 / 状态色（dataviz 校验器实跑通过，light · surface #fffdfb）
- 币种身份色（分类色，`--pairs all` 全对 PASS，最差 deutan ΔE 15.1，浅底同样通过）：
  JUNO `#b98a00` · DragonX `#00a0a6` · midstate `#8451c9`
  —— DragonX 从红改为青（**2026-08-30 用户已接受**）：青是品牌标识色，且与状态红/孤块环不再混淆。
  上生产时改 `config.js` 各币 `color`（juno `#d9a62e`→`#b98a00`、dragonx `#e34948`→`#00a0a6`、midstate `#7c5cff`→`#8451c9`），币卡色线与曲线自动跟随。
- 状态色（固定，不随主题）：confirmed `#0ca30c`（3.3:1，徽章字用 `#006a20`）· pending `#eda100`（低对比，**标记加 `#935b00` 墨环** + 图标/文字/tooltip 补契）· orphaned `#d03b3b`（4.7:1，**渲染为空心环**）· solo = 菱形（形状，不占颜色）。
- 曲线：2px 单调三次样条，面积填充 ≤ 18%→0 渐隐，横向 hairline 网格，端点 8px 圆 + 2px 面色环；单序列不放图例，十字线 + tooltip 默认开；文字永远用文字 token，不穿数据色；窄屏 x 轴只留 3 个刻度。

## 语言（用户拍板：中英互换 + 按浏览器语言默认）
- 判定顺序：URL `?lang=zh|en`（调试/e2e 用）→ `localStorage.ntm_lang`（与生产站同 key，手动选择后持久）→ `navigator.languages` 里任一以 `zh` 开头 → 中文，否则英文。
- 头部「中文 / EN」按钮即切换，整页重渲染并 `dispatchEvent('ntm:language')`（与生产 notice.js 的监听约定一致）；`<html lang>` 同步。
- 界面文案走 `I18N[lang]` 词表；config 自带的 `{zh,en}` 字段走 `L()`，`cli.groups` 的 `*Zh/*En` 字段走 `pick()`；缺英文回退中文（不留空）。
- 生产实现：沿用现有 `i18n.*.js` 的 `t()` + `translateStatus()` 白名单机制（⚠ 加词条必须同时加白名单，见 pitfall《前端i18n加词条不等于修好》）。

## 下载页（用户拍板：卡片模式，卡片可点进去）
- `/download`：锄头**卡片**（顶部 3px 锄头色线、logo、名/版本/发布日、抽水徽章、通用/专用 + CPU/GPU chips、可挖/平台）+「按币找锄头」表。
- `/download/<id>`：页头 → 摘要条 → 分区跳转（下载文件 / 一键上手 / 完整参数 / 说明 / FAQ / 更新记录）
  → 文件卡（运行文件名、大小、sha256 + zip 内 sha256、校验命令、下载按钮）
  → **一键上手生成器**（矿池/币 → 接入点 → 模式 → 收款地址（按 pattern 软校验）→ 矿机名 ⇒ Windows 命令 / 一键 .bat / Linux 命令 / 常驻 mine.sh 四框同步）
  → 完整参数（事实源 = 上架二进制实跑 `--help`；用法 / 示例 / 分组表）→ 运行要求 + 要点 → FAQ（details）→ 更新记录。
- 数据全部来自生产 `config.js` 的 `downloads.miners[]`，与现网同一份注册表。

## Typography
- Display / Body：**Geist** 600 / 400（Google Fonts；生产自托管 latin 子集，CJK 回退 PingFang / 微软雅黑 / Noto Sans CJK）
- Mono：**JetBrains Mono** 400–600 —— 地址、hash、命令、表头、micro-label、坐标轴
- 大数字：Geist 600，`tabular-nums` 只用于列对齐（表格、坐标轴），stat 大字用比例数字
- 标签：JBM 11px UPPERCASE +.08em，`--ink-3`
- 标题字距 -.02em；无斜体标题

## 组件语言
- 半径：面板 16px · 元件 10px · chip/胶囊 999px
- 卡：`paper-2` + 1px `--rule`；hover 只换边线为 `--rule-2`
- 按钮：主 = `--accent` 实心 10px；次 = 透明 + `--rule-2` 边；八态齐全，focus 环即时出现不做过渡
- 命令框：`paper-3` 深面 + 功能性标题行 + 复制按钮（**不画 mac 窗口点**）
- 表格：thead mono uppercase 11px；行 hairline；≤ 640px 塌缩为卡片（`data-label`）
- 徽章：tint 底 + 同族字 + 图标（点 / 空心环），永不裸色

## Motion
- 路由切换一次 220ms 淡入（**数据 20s 自刷新不重放**）；live 点 2.4s 呼吸（opacity）
- hover/active 只变颜色与 1px 位移；easing `cubic-bezier(.16,1,.3,1)`；`prefers-reduced-motion` 全静

## 移动端硬指标
320/375/414/768 零横向滚动；`html,body{overflow-x:clip}`；头部两行（lockup+CTA / 币种 chips 横向滚动）；
stat band 4→2→1；bento 全部 span 1；表格塌缩；点击目标 ≥ 40px

## 部署铁律（沿用 v2）
改前先 scp 拉生产基线；js/css 换 release 名并同步 index.html；config.js/index.html 原名改；
上线后公网 sha256 对拍 + 无头 Chrome 真站渲染；移动端验收用 CDP 设备仿真或 iframe 框架页
