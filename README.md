# NTMPool — 通用多币种矿池核心

> 工厂级统一矿池核心。目标：**一套核心，接入所有币**，功能全面超越 miningcore。
> 私有仓库：`scashcc/ntmpool`（闭源，只发二进制/镜像）。

## 这个项目是什么

工厂已经为 9+ 个币做过矿池：有的魔改 miningcore（dragonx/zoka/flowcoin/taron），有的整池自写（bitcoin09 Go 池 / btx Rust 池 / quantus QUIC 桥 / midstate）。每个币都重复踩一遍「stratum + vardiff + 会计 + 打款 + 孤块 + API」的全套坑。

**NTMPool 把这些实战经验收敛成一个统一核心**：新币接入只需写一个「币适配器」（节点对接 + 算法哈希库），其余全部复用。

## 与 miningcore 的核心差异（为什么要自写）

| 痛点 | miningcore | NTMPool |
|---|---|---|
| 非标准链接入 | 必须继承 family 基类，样板极多，节点无 RPC 直接没法接 | 节点适配层与算法层**正交解耦**，HTTP/QUIC/内嵌节点/RPC 都是一等公民 |
| API 隐私 | 前端 API 直接暴露矿工完整钱包地址（抓包可得） | 对外 API 地址脱敏，矿工凭完整地址/密码查自己 |
| 打款可信度 | 只记 txid，不追踪 tx 确认，不知道打没打成 | 打款状态机：sent → tx 确认追踪 → confirmed/failed，全程可查 |
| 孤块 | ClassifyBlocks 有但入账后不再管 | 成熟入账前**块 ID 逐字节比对主链** + 孤块作废 + debts 追缴（bitcoin09 事故的完整修法内建） |
| 热配置 | 改 config 要重启 | 手续费/端口/难度/打款参数**热更新**，管理后台 API 直改 |
| 横向扩展 | 单实例 | 多实例共享 Postgres，前端 API 聚合多台服务器 |
| vardiff | 无一步 grace，GPU 批量提交会良性拒单 | 内建一步难度 grace + retarget 限频 + lowdiff/badpow 拆分（midstate 修法内建） |
| 双地址安全 | 无 | 矿池地址 + 手续费地址强制分离，手续费自动归集 + 一键转移 |

## 目录结构

```
pool-core/
    README.md            ← 本文件
    docs/
        01-需求规格.md    ← 完整功能需求（老板 14 条 + 补充）
        02-架构设计.md    ← 技术架构（四层解耦 + 数据模型 + 协议面）
        03-ROADMAP.md     ← 分期实施计划
    src/                 ← Go 源码（git 仓库根 = pool-core/）
```

## 铁律（继承工厂 CLAUDE.md，矿池核心追加）

1. **share 校验 = 池端重算，且与节点用同一份共识哈希库**——零字节分歧是最大安全保证。
2. **孤块校验必须第一天就有**（成熟入账前块 ID 逐字节比对，不是高度比对）。
3. **打款先扣余额再发 tx，失败绝不自动重发**；批量必须单笔 SendMany，绝不逐地址串行。
4. **`payouts=false` 开关第一天就有**——出事能只停打款不停挖矿。
5. **算力统计窗口与 PPLNS 打款窗口解耦**，绝不复用同一个被裁剪的 share 数组。
6. 全网算力用 getnetworkhashps 真算法（最近 N 块 work ÷ 实际时间跨度）。
7. 密钥/钱包/PAT 绝不进 git。

## 相关知识库

- `../_knowledge/pitfalls/` — 22 条矿池相关踩坑（本项目设计已全部规避，见 docs/02 附录）
- 各币池实现参考：`../coins/bitcoin09/pool/`（Go，会计/孤块/debts）、`../coins/btx/pool/`（Rust，Postgres schema/防双花打款）、`../coins/quantus/pool/`（QUIC 桥）、`coins/zoka/pool/mc-zoka/`（miningcore 自定义 HTTP family）
