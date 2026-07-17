/* NTM pool frontend — 唯一编辑点：品牌 + 币种注册表。
 * 动态数据一律来自 /api（NTMPool 公共 API），此处只放静态元信息。 */
window.NTM_CONFIG = {
  brand: {
    // NTM 英文全称（改这里即可全站生效）
    expansionEn: 'Next-Tier Mining',
    minerVersion: 'v1.19.0',
  },
  // 矿工软件下载（自托管于 www.ntmminer.com/downloads/，闭源只发二进制）。
  // 加平台/换版本改这里；sha256 用 `sha256sum <文件>` 现算后填，方便矿工核验。
  // status: 'ready'=有真实文件可下载；'building'=暂未提供（页面显示「构建中」，不给死链）。
  downloads: {
    version: 'v1.19.0',
    baseUrl: '/downloads',
    files: [
      { os: 'Windows x64', icon: 'win', file: 'NTMminer-windows-x64.exe', status: 'ready',
        sha256: '49eefdd8b44cbc2af87168378a4691b2637654a1e6e99047aa7b72a4abb3fa00',
        noteZh: 'Windows 10/11 64 位。含 CPU + NVIDIA GPU 后端，原生多显卡（--gpu-devices）。本版新增 Velkar (VELK) 的 NVIDIA GPU 挖矿支持。单文件静态构建，无需任何额外 DLL，解压即用。首次运行若被 Defender 拦截，选「仍要运行」。',
        noteEn: 'Windows 10/11 64-bit. CPU + NVIDIA GPU backends, native multi-GPU (--gpu-devices). This release adds NVIDIA GPU mining for Velkar (VELK). Single static executable, no extra DLLs, no install needed. If Defender warns, choose “Run anyway”.' },
      { os: 'Linux x64 / HiveOS', icon: 'linux', file: 'NTMminer-linux-x64', status: 'ready',
        sha256: '4106a70a91bbd09a6a51fb9c3e07244a5171e5ba4947a014b2ee616d2b017738',
        noteZh: '通用 x86-64 Linux（Ubuntu/Debian/HiveOS 等）。含 CPU + NVIDIA GPU 后端，原生多显卡（--gpu-devices）。本版新增 Velkar (VELK) 的 NVIDIA GPU 挖矿支持。下载后 chmod +x 即可运行。',
        noteEn: 'Generic x86-64 Linux (Ubuntu/Debian/HiveOS). CPU + NVIDIA GPU backends, native multi-GPU (--gpu-devices). This release adds NVIDIA GPU mining for Velkar (VELK). chmod +x and run.' },
    ],
  },
  api: { base: '/api', refreshMs: 20000 },
  // key = NTMPool 池 id（/api/pools 里的 id）。只有出现在 /api/pools 里的币才会显示为在线。
  coins: {
    dragonx: {
      symbol: 'DRGX',
      name: 'DragonX',
      algo: 'rx/dragonx',
      color: '#e34948',
      logo: '/img/dragonx.png',
      hashUnit: 'H/s',
      // 矿工公网入口（不暴露矿池真实 IP）
      stratum: [
        { host: 'hk.ntmminer.com', port: 3333 },
        { host: 'hk.ntmminer.com', port: 4444 },
        { host: 'hk.ntmminer.com', port: 5555 },
        { host: 'hk.ntmminer.com', port: 7777, mode: 'solo' },
      ],
      ntmAlgoFlag: 'rx/dragonx',
      addressPrefix: 'zs1',
      addressExample: 'zs1qknw...(你的 Sapling zs 地址)',
      xmrigCompatible: true, // drg-xmrig / xmrig 系可直连
      confirmations: 10,
      links: {
        site: 'https://dragonx.is',
        git: 'https://git.dragonx.is/DragonX/dragonx',
      },
      descEn: 'DragonX (DRGX) is a privacy chain in the Hush / Komodo family. PoW is the rx/dragonx RandomX variant — CPU friendly, ASIC resistant. Miners use shielded zs addresses for both login and payouts.',
      descZh: 'DragonX（DRGX）是 Hush / Komodo 系的强隐私链，PoW 为 RandomX 变体 rx/dragonx —— CPU 友好、抗 ASIC。矿工登录与收款均使用 zs 隐私地址。',
    },
    btc09: {
      symbol: '09C',
      name: 'Bitcoin 09',
      algo: 'Argon2id (64 MiB)',
      color: '#f7931a',
      logo: '/img/bitcoin09.png',
      hashUnit: 'H/s',
      // 独立 NTMPool 实例：前端经 /api-btc09 反代（nginx→隧道→池 4410）
      apiBase: '/api-btc09',
      stratum: [
        { host: 'hk.ntmminer.com', port: 8344 },
        { host: 'hk.ntmminer.com', port: 8345, mode: 'solo' },
      ],
      ntmAlgoFlag: 'btc09',
      addressPrefix: '4',
      addressExample: '4k26Vj...(你的 09C 地址)',
      xmrigCompatible: false, // 09C 是 NTMminer 专属线协议
      gpuMulti: true, // Argon2id 64MiB GPU 友好（NTMminer v1.17.0 起）：Connect 页出「多显卡挖矿」教程
      confirmations: 10,
      links: {
        site: 'https://btc09.org',
        git: 'https://github.com/krutftw/bitcoin09',
      },
      descEn: 'Bitcoin 09 (09C) is a clean-room Go rewrite of Bitcoin with one change: PoW is Argon2id at 64 MiB per hash — memory-hard. Mine it on CPU or, from NTMminer v1.17.0, on NVIDIA GPU: the 64 MiB / t=1 parameters are bandwidth-bound and GPU-friendly (a single card ≈ dozens of CPU cores; concurrency is capped by VRAM, 64 MiB per hash). 21M cap, 50-coin subsidy, 10-minute blocks, halving every 210,000 blocks.',
      descZh: 'Bitcoin 09（09C）是 Bitcoin 的 clean-room Go 重写，只改一处：PoW 换成 64 MiB Argon2id —— 内存硬。CPU 可挖，NTMminer v1.17.0 起也支持 NVIDIA GPU：64 MiB / t=1 属带宽 bound、GPU 友好（单卡算力约等于数十个 CPU 核；并行度受显存限制，每个 hash 占 64 MiB）。2100 万上限、50 币补贴、10 分钟出块、每 21 万块减半，经济模型与比特币逐条一致。',
    },
    noctari: {
      symbol: 'NCTI',
      name: 'Noctari',
      algo: 'Quark',
      color: '#8b7cf6',
      logo: '/img/noctari.png',
      hashUnit: 'H/s',
      // 独立 NTMPool 实例：前端经 /api-noctari 反代（nginx→隧道→池 4020）
      apiBase: '/api-noctari',
      stratum: [
        { host: 'hk.ntmminer.com', port: 4455 },
        { host: 'hk.ntmminer.com', port: 4456, mode: 'solo' },
      ],
      ntmAlgoFlag: 'quark',
      addressPrefix: 'N',
      addressExample: 'Ngar...(你的 NCTI 地址)',
      xmrigCompatible: false, // Noctari 走 NTMminer 专属 cnjob 线协议
      gpuMulti: true, // Quark GPU 友好：Connect 页出「多显卡挖矿」教程
      confirmations: 100,
      links: {
        site: 'https://github.com/noctari-core/noctari',
        git: 'https://github.com/noctari-core/noctari',
      },
      descEn: 'Noctari (NCTI) is a PIVX v5 fork mined with the Quark algorithm (a 9-round hash chain of blake/bmw/groestl/jh/keccak/skein) — CPU and GPU friendly. Fair launch: blocks 1–20159 are a ~7-day PoW window (50 NCTI per block, entirely to miners, zero premine) after which the chain switches to PoS permanently. This pool settles via PPLNS: block rewards accrue to the pool wallet and are paid out by share weight once coinbase matures at 100 confirmations.',
      descZh: 'Noctari（NCTI）是 PIVX v5 fork，用 Quark 算法（blake/bmw/groestl/jh/keccak/skein 九轮链式哈希）挖矿 —— CPU、GPU 均可。公平启动：区块 1–20159 为约 7 天的 PoW 窗口（50 NCTI/块全归矿工、零 premine），窗口结束后永久转 PoS。本池 PPLNS 结算：爆块奖励先进矿池钱包，coinbase 满 100 确认成熟后按 share 占比打款给矿工。',
    },
    velkar: {
      symbol: 'VELK',
      name: 'Velkar',
      algo: 'VelkarHash',
      color: '#6366f1',
      hashUnit: 'H/s',
      // 独立 NTMPool 实例：前端经 /api-velkar 反代（nginx→隧道→池 4412）
      apiBase: '/api-velkar',
      stratum: [
        { host: 'hk.ntmminer.com', port: 5511 },
        { host: 'hk.ntmminer.com', port: 5512, mode: 'solo' },
      ],
      ntmAlgoFlag: 'velkarhash',
      addressPrefix: 'velkar:',
      addressExample: 'velkar:qz...(你的 velkar 地址)',
      xmrigCompatible: false, // VelkarHash 故意反通用锄头，唯一支持=NTMminer
      gpuMulti: true, // Argon2id 8MiB GPU 友好（NTMminer v1.19.0 起）：Connect 页出「多显卡挖矿」教程
      confirmations: 30,
      // ★不打款声明：本池被承包用于测试，收益不发放。noPayout=true 时
      // 币页顶部横幅 + Connect 页结算面板都显示此声明，绝不显示打款规则。
      noPayout: true,
      noticeZh: '⚠ 本矿池不打款：VELK 矿池已被大佬整体承包用于测试，所有爆块收益归承包方，不向矿工发放。请知悉后再决定是否接入。',
      noticeEn: '⚠ NO PAYOUTS: this VELK pool is fully contracted by a private sponsor for testing. All block rewards go to the sponsor — nothing is paid out to miners. Please mine here only if you understand this.',
      links: {
        site: 'https://github.com/VelkarVELK',
        git: 'https://github.com/VelkarVELK/velkar-wallet',
      },
      descEn: 'Velkar (VELK) is a Kaspa (rusty-kaspa) BlockDAG fork with a custom VelkarHash PoW: keccak-f1600 → a 64×64 matrix heavy-hash → an Argon2id 8 MiB memory-hard stage. The extra stage is deliberately incompatible with generic Kaspa miners — NTMminer is the only supported miner (CPU, and NVIDIA GPU from v1.19.0). The chain relaunched from a new genesis on 2026-07-15. Note: this pool is contracted for testing and does NOT pay out.',
      descZh: 'Velkar（VELK）是 Kaspa（rusty-kaspa）BlockDAG fork，PoW 为魔改 VelkarHash：keccak-f1600 → 64×64 矩阵 heavy-hash → Argon2id 8 MiB 内存硬阶段。多出的阶段故意与通用 Kaspa 锄头不兼容 —— 唯一支持的锄头是 NTMminer（CPU 可挖，v1.19.0 起支持 NVIDIA GPU）。链已于 2026-07-15 从新 genesis 重启。注意：本池被承包用于测试，不打款。',
    },
  },
};
