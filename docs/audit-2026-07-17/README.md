# 2026-07-17 底层深度审计 — codex 9 轮问答原始答案

蒸馏结论与修复方案见上级目录 `../10-底层审计-差距清单与修复方案.md`。
本目录是原始素材（约 30 万字），供深挖细节时查阅。

用 codex(gpt-5.6-sol，重头戏 max、其余 xhigh)以「从零设计一个生产级多币矿池」姿势逼问 9 轮，
每轮联网核查行业事故后作答。

| 文件 | 主题 | 亮点 |
|---|---|---|
| q1_answer.md | 整体架构与进程模型 | 三平面(Work/Accounting/Money)、多币建模、加新币理想改动面、第一天必做对的决策 |
| q2_answer.md | **打款安全** | 完整状态机、重复打款三防、超时不重发、双向对账、追回平账、10条铁律、NiceHash/MPOS/Monero事故 |
| q3_answer.md | 协议兼容 | 方言对照表、按端口vs嗅探、ASIC断连雷区、NiceHash/代理nonce分层、vardiff生效时机、SV2该不该上 |
| q4_answer.md | **端口攻击/拉黑IP/转发隐藏** | ban阈值表、PROXY信任边界、点评我方MASQUERADE连坐事故、分层防御参考架构 |
| q5_answer.md | 会计/对账/孤块/算力 | PPLNS累计work数学、PPS准备金公式(Rosenfeld)、复式记账分录表、effort五层漏斗、Prohashing/BIP66事故 |
| q6_answer.md | 节点对接/出块检测 | 五形态适配、多节点failover、submitblock归一化、reorg检测、模板强制字段校验 |
| q7_answer.md | 运维/崩溃恢复/多实例 | 重启恢复固定顺序+清单、意图先落库、leader fencing token、监控告警阈值、磁盘保护分级、安全域四区 |
| q8_answer.md | **真实坑复盘** | 把我方12个真实坑(A~L)抛给它独立复盘根因+架构级根治+通用铁律，另补M~W经典坑 |
| q9_answer.md | 收官清单(验收标准) | 22致命+20重要+12进阶、Gate 0/S/A/P/O上线门禁、10条不变量、M0~M9里程碑、审计5大必查点 |

codex_batch1.md / codex_batch2.md = 据此派给 codex 的两批加固任务书（已实施，见 ../10 §3）。
