/* NTM pool frontend — 唯一编辑点：品牌 + 币种注册表。
 * 动态数据一律来自 /api（NTMPool 公共 API），此处只放静态元信息。 */
window.NTM_CONFIG = {
  brand: {
    // NTM 英文全称（改这里即可全站生效）
    expansionEn: 'Next-Tier Mining',
    minerVersion: 'v1.13.0',
  },
  // 矿工软件下载（自托管于 www.ntmminer.com/downloads/，闭源只发二进制）。
  // 加平台/换版本改这里；sha256 用 `sha256sum <文件>` 现算后填，方便矿工核验。
  // status: 'ready'=有真实文件可下载；'building'=暂未提供（页面显示「构建中」，不给死链）。
  downloads: {
    version: 'v1.13.0',
    baseUrl: '/downloads',
    files: [
      { os: 'Windows x64', icon: 'win', file: 'NTMminer-windows-x64.exe', status: 'ready',
        sha256: 'c22faaaa9903aee968b506ac3d97c4fa6402057b353450ca371055c78774ede7',
        noteZh: 'Windows 10/11 64 位。含 CPU + NVIDIA GPU 后端。解压即用，无需安装。首次运行若被 Defender 拦截，选「仍要运行」。',
        noteEn: 'Windows 10/11 64-bit. CPU + NVIDIA GPU backends. No install needed. If Defender warns, choose “Run anyway”.' },
      { os: 'Linux x64 / HiveOS', icon: 'linux', file: 'NTMminer-linux-x64', status: 'ready',
        sha256: '8f04532e3b3ed4b3d38f5536a72512f0b727c8bf975a538e74621669ff7530f1',
        noteZh: '通用 x86-64 Linux（Ubuntu/Debian/HiveOS 等）。含 CPU + NVIDIA GPU 后端。下载后 chmod +x 即可运行。',
        noteEn: 'Generic x86-64 Linux (Ubuntu/Debian/HiveOS). CPU + NVIDIA GPU backends included. chmod +x and run.' },
      { os: 'Linux ARM64', icon: 'linux', file: 'NTMminer-linux-arm64', status: 'ready',
        sha256: 'b17f8e9380f52b963b2b35bc8c0f3b37754b28ac7a39869246406fd4585859b5',
        noteZh: '64 位 ARM Linux（ARM 云服务器 / 树莓派 4/5 等）。CPU-only、静态链接、无依赖。下载后 chmod +x 即可运行。',
        noteEn: '64-bit ARM Linux (ARM cloud servers / Raspberry Pi 4/5). CPU-only, static, no dependencies. chmod +x and run.' },
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
        { host: 'hk.dragonx.cc', port: 3333 },
        { host: 'hk.dragonx.cc', port: 4444 },
        { host: 'hk.dragonx.cc', port: 5555 },
        { host: 'hk.dragonx.cc', port: 7777, mode: 'solo' },
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
      confirmations: 10,
      links: {
        site: 'https://btc09.org',
        git: 'https://github.com/krutftw/bitcoin09',
      },
      descEn: 'Bitcoin 09 (09C) is a clean-room Go rewrite of Bitcoin with one change: PoW is Argon2id at 64 MiB per hash — memory-hard, CPU-only, ASIC/GPU hostile. 21M cap, 50-coin subsidy, 10-minute blocks, halving every 210,000 blocks.',
      descZh: 'Bitcoin 09（09C）是 Bitcoin 的 clean-room Go 重写，只改一处：PoW 换成 64 MiB Argon2id —— 内存硬、CPU 专属、天然抗 ASIC/GPU。2100 万上限、50 币补贴、10 分钟出块、每 21 万块减半，经济模型与比特币逐条一致。',
    },
    midstate: {
      symbol: 'MDS',
      name: 'Midstate',
      algo: 'BLAKE3 VDF',
      color: '#4fd1c5',
      hashUnit: 'ext/s', // 1 ext = 1,000,000 次 BLAKE3（VDF 全量），与 NTMminer 显示同口径
      // 独立 NTMPool 实例：前端经 /api-mds 反代（nginx→隧道→池 4411）
      apiBase: '/api-mds',
      stratum: [
        { host: 'hk.tutuit.xyz', port: 3333 },
      ],
      ntmAlgoFlag: 'midstate',
      // 金额单位制（二进制阶梯）：1 kMDS=1024 / 1 mMDS=2^20 / 1 gMDS=2^30 MDS；
      // API 十进制串 ×1e9 = 链上最小单位 MDS 数（块奖励 2^30 = 正好 1 gMDS）
      amountScale: 1e9,
      binaryUnits: true,
      directPayout: true, // coinbase 直付：结算文案与「起付额→尘埃阈值」按此切换
      addressPrefix: '',
      addressExample: 'c15363...(你的 64 位 hex midstate 地址)',
      xmrigCompatible: false, // midstate 是 NTMminer 专属线协议
      confirmations: 8,
      links: {
        site: 'https://github.com/ciphernom/midstate',
        git: 'https://github.com/ciphernom/midstate',
      },
      descEn: 'Midstate (MDS) is a post-quantum chain (WOTS/MSS signatures) with an iterated-BLAKE3 sequential PoW — GPU friendly. This pool pays out directly inside the block: each block\'s coinbase is split to miner addresses at template time (PPLNS), so rewards arrive the moment a block is found — no pool-side transfers, no payout delay. MDS has no coinbase maturity: coins are spendable immediately.',
      descZh: 'Midstate（MDS）是后量子签名链（WOTS/MSS），PoW 为迭代 BLAKE3 顺序哈希 —— GPU 友好。本池采用 coinbase 直付：模板构建时就按 PPLNS 把块奖励拆分直接付到矿工地址，「爆块即到账」—— 无池端转账、无打款延迟；且 MDS 无 coinbase 成熟期，到账即可花。',
    },
  },
};
