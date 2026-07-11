-- NTMPool PostgreSQL schema
-- 形状对齐 miningcore（列名全小写、金额 NUMERIC 币为单位）以便复用其前端/工具，
-- 并做了关键扩展：debts（孤块追缴）、payments 确认追踪、config_audit、instances。
-- share 明细只留短期（默认 3 天，够 PPLNS 窗口重建），审计类表永久保留。

CREATE TABLE IF NOT EXISTS shares (
    id           BIGSERIAL PRIMARY KEY,          -- 插入序 = PPLNS 窗口回溯序（稳定 tiebreaker）
    poolid       TEXT        NOT NULL,
    blockheight  BIGINT      NOT NULL,
    difficulty   DOUBLE PRECISION NOT NULL,  -- 计权难度（一步 grace 下可能是 prevDiff）
    networkdifficulty DOUBLE PRECISION NOT NULL DEFAULT 0,
    miner        TEXT        NOT NULL,
    worker       TEXT        NULL,
    useragent    TEXT        NULL,
    ipaddress    TEXT        NOT NULL,       -- PROXY protocol 还原的真实 IP
    source       TEXT        NULL,           -- 实例 ID（多服务器）
    solo         BOOLEAN     NOT NULL DEFAULT FALSE, -- solo 端口 share：只记录不进 PPLNS 窗口
    created      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_shares_pool_created ON shares(poolid, created);
CREATE INDEX IF NOT EXISTS idx_shares_pool_miner ON shares(poolid, miner, created);
-- 老库迁移（幂等）：v2026-07-11 solo 混跑修复前建的 shares 表没有 solo 列
ALTER TABLE shares ADD COLUMN IF NOT EXISTS solo BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS blocks (
    id           BIGSERIAL PRIMARY KEY,
    poolid       TEXT        NOT NULL,
    blockheight  BIGINT      NOT NULL,
    networkdifficulty DOUBLE PRECISION NOT NULL,
    status       TEXT        NOT NULL,       -- submitting | pending | confirmed | orphaned | submit-failed
                                             -- submitting=「先记后交」意图记录：崩溃恢复扫描它与链上比对（docs/05 场景B）
    type         TEXT        NOT NULL DEFAULT 'block', -- block|uncle（唯一键含 type，防 miningcore #1600 撞约束崩打款）
    confirmationprogress DOUBLE PRECISION NOT NULL DEFAULT 0, -- 0~1，前端可显示成熟进度
    effort       DOUBLE PRECISION NULL,
    minereffort  DOUBLE PRECISION NULL,
    transactionconfirmationdata TEXT NOT NULL, -- 我们提交的块 hash —— 孤块逐字节比对基准
    miner        TEXT        NULL,            -- 爆块者
    worker       TEXT        NULL,
    solo         BOOLEAN     NOT NULL DEFAULT FALSE,
    reward       NUMERIC NULL,
    feeamount    NUMERIC NULL,                   -- confirm 时计提的手续费；孤块随 status 翻转自动出账
    rawhex       TEXT        NULL,               -- 提交的原始块 hex（崩溃重播/审计；docs/05 场景B）
    source       TEXT        NULL,
    created      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (poolid, blockheight, type, transactionconfirmationdata)
);

CREATE TABLE IF NOT EXISTS balances (
    poolid       TEXT        NOT NULL,
    address      TEXT        NOT NULL,
    amount       NUMERIC NOT NULL DEFAULT 0,
    created      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (poolid, address)
);

-- 审计流水：每一次余额变动一行（入账/打款/追缴/补发/手工调整）。永久保留，对账生命线。
CREATE TABLE IF NOT EXISTS balance_changes (
    id           BIGSERIAL PRIMARY KEY,
    poolid       TEXT        NOT NULL,
    address      TEXT        NOT NULL,
    amount       NUMERIC NOT NULL,     -- 正=入账 负=扣减
    usage        TEXT        NULL,           -- reward|payment|orphan_debt|debt_repay|manual_credit|manual_debit|fee
    tags         TEXT[]      NULL,           -- 关联对象，如 block:123 / payment:45 / admin:xxx
    created      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_balance_changes_pool_addr ON balance_changes(poolid, address, created);

-- 分账快照：confirm 时每地址应得（抵债前全额，聪语义的十进制）。孤块回滚的唯一依据
-- （对应内存实现的 memBlock.payouts）。orphan 后行保留、reversed 置真（审计可溯）。
CREATE TABLE IF NOT EXISTS block_credits (
    poolid       TEXT    NOT NULL,
    blockhash    TEXT    NOT NULL,
    address      TEXT    NOT NULL,
    amount       NUMERIC NOT NULL,
    reversed     BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (poolid, blockhash, address)
);

-- 孤块追缴（bitcoin09 事故机制的产品化）：预打款垫付的块孤了 → 生成欠款，从未来收益抵扣。
CREATE TABLE IF NOT EXISTS debts (
    id           BIGSERIAL PRIMARY KEY,
    poolid       TEXT        NOT NULL,
    address      TEXT        NOT NULL,
    original     NUMERIC NOT NULL,     -- 原始欠款
    remaining    NUMERIC NOT NULL,     -- 尚未抵扣
    reason       TEXT        NOT NULL,       -- 如 orphaned block 12345
    created      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_debts_pool_addr ON debts(poolid, address) WHERE remaining > 0;

-- 打款：完整状态机（miningcore 只记 txid 的空白正是这里补上的）。
-- created(已扣余额,txid NULL) → sent → confirming → confirmed / failed（人工介入，绝不自动重发）
CREATE TABLE IF NOT EXISTS payments (
    id           BIGSERIAL PRIMARY KEY,
    poolid       TEXT        NOT NULL,
    address      TEXT        NOT NULL,
    amount       NUMERIC NOT NULL,
    batchid      BIGINT      NOT NULL,       -- 同一笔 sendmany 的所有输出共享 batchid（幂等键，防重启重发）
    transactionconfirmationdata TEXT NULL,   -- txid
    status       TEXT        NOT NULL DEFAULT 'created',
    confirmations BIGINT     NOT NULL DEFAULT 0,
    created      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (poolid, batchid, address)            -- Save 幂等 UPSERT 键
);
CREATE INDEX IF NOT EXISTS idx_payments_pool_addr ON payments(poolid, address, created);
CREATE INDEX IF NOT EXISTS idx_payments_pool_status ON payments(poolid, status) WHERE status <> 'confirmed';

CREATE TABLE IF NOT EXISTS payment_batches (
    id           BIGSERIAL PRIMARY KEY,
    poolid       TEXT        NOT NULL,
    kind         TEXT        NOT NULL DEFAULT 'payout', -- payout | fee_collect | fee_sweep | consolidate(钱包整备:note/UTXO合并)
    -- 状态机：created(已扣余额) → prepared(已签名未广播,txid已确定) → sent → confirming → confirmed
    --         / failed(人工介入) / unknown(不支持rawtx的链崩溃窗口,冻结打款+人工比对, docs/05 场景A)
    status       TEXT        NOT NULL DEFAULT 'created',
    plannedtxid  TEXT        NULL,           -- 签名即定的 txid（广播前落库 —— 崩溃恢复零歧义的关键）
    rawtx        TEXT        NULL,           -- 已签名原始交易：恢复时可原样重播（同 txid 天然幂等防双花）
    txid         TEXT        NULL,           -- 实际广播确认的 txid（正常 == plannedtxid）
    total        NUMERIC NOT NULL,
    created      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 矿工自助设置（-p mp=21 密码绑定，docs/01 R5）
CREATE TABLE IF NOT EXISTS miner_settings (
    poolid       TEXT        NOT NULL,
    address      TEXT        NOT NULL,
    paymentthreshold NUMERIC NULL,
    passwordhash TEXT        NULL,           -- 首次带密码连接即绑定；改设置需同一密码
    accesskey    TEXT        NULL,           -- 私密面板链接 token（Braiins 模式）
    notify       JSONB       NULL,           -- 预留：telegram/email
    created      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (poolid, address)
);

-- 算力时序：60s 打点 → 10min 桶。明细保留 24h（R6），日汇总长期。
CREATE TABLE IF NOT EXISTS minerstats (
    poolid       TEXT        NOT NULL,
    miner        TEXT        NOT NULL,
    worker       TEXT        NOT NULL DEFAULT '',
    hashrate     DOUBLE PRECISION NOT NULL,
    sharespersecond DOUBLE PRECISION NOT NULL DEFAULT 0,
    created      TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_minerstats_pool_miner ON minerstats(poolid, miner, created);
-- 桶持久化幂等写（statspersist：ON CONFLICT DO NOTHING 的冲突目标）
CREATE UNIQUE INDEX IF NOT EXISTS uq_minerstats_bucket ON minerstats(poolid, miner, worker, created);

CREATE TABLE IF NOT EXISTS poolstats (
    poolid       TEXT        NOT NULL,
    connectedminers INT      NOT NULL,
    poolhashrate DOUBLE PRECISION NOT NULL,
    networkhashrate DOUBLE PRECISION NOT NULL, -- getnetworkhashps 真算法（铁律）
    networkdifficulty DOUBLE PRECISION NOT NULL,
    lastnetworkblocktime TIMESTAMPTZ NULL,
    blockheight  BIGINT      NOT NULL DEFAULT 0,
    created      TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_poolstats_pool ON poolstats(poolid, created);
CREATE UNIQUE INDEX IF NOT EXISTS uq_poolstats_bucket ON poolstats(poolid, created);

-- 多实例注册表（多服务器横向扩展，R14.2）
CREATE TABLE IF NOT EXISTS instances (
    id           TEXT PRIMARY KEY,            -- pool1 / pool2 …
    hostname     TEXT NOT NULL,
    version      TEXT NOT NULL,
    coins        TEXT[] NOT NULL,
    lastseen     TIMESTAMPTZ NOT NULL
);

-- 配置变更审计（谁在何时把费率从 10% 改成 5%，R14.11）
CREATE TABLE IF NOT EXISTS config_audit (
    id           BIGSERIAL PRIMARY KEY,
    actor        TEXT NOT NULL,               -- admin token 名 / system
    coin         TEXT NULL,
    field        TEXT NOT NULL,
    oldvalue     TEXT NULL,
    newvalue     TEXT NULL,
    created      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 守恒对账快照（R8）：Σ已确认奖励 = Σ已付 + Σ余额 + Σ手续费 + Σ在途 + Σ债务净额
CREATE TABLE IF NOT EXISTS reconciliations (
    id           BIGSERIAL PRIMARY KEY,
    poolid       TEXT NOT NULL,
    confirmed_rewards NUMERIC NOT NULL,
    total_paid   NUMERIC NOT NULL,
    total_balances NUMERIC NOT NULL,
    total_fees   NUMERIC NOT NULL,
    in_flight    NUMERIC NOT NULL,
    debts_net    NUMERIC NOT NULL,
    delta        NUMERIC NOT NULL,      -- 应为 0，非 0 告警并冻结打款
    created      TIMESTAMPTZ NOT NULL DEFAULT now()
);
