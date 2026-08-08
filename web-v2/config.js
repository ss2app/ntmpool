window.NTM_CONFIG = {
  schemaVersion: 1,

  brand: {
    siteName: 'NTMminer Pools',
    productName: 'NTMminer',
    expansionEn: 'Next-Tier Mining',
    canonicalOrigin: 'https://www.ntmminer.com',
    assets: {
      master: '/img/logo-ref.png',
      mark: '/img/ntm-mark.svg',
      lockup: '/img/ntmminer-pools-light.svg',
      favicon: '/img/favicon.svg',
      ogImage: '/img/og-ntmminer-pools.png',
    },
    hero: {
      title: { zh: 'NTMminer Pools', en: 'NTMminer Pools' },
      subtitle: {
        zh: '自研矿工软件与透明的多币种矿池。',
        en: 'In-house mining software and transparent multi-coin pools.',
      },
    },
    links: {
      discord: 'https://discord.gg/kTJe839y37',
    },
  },

  miner: {
    devFeePercent: 0,
    nativeMultiGpuSince: 'v1.14.0',
  },

  api: {
    base: '/api',
    refreshMs: 20000,
    timeoutMs: 10000,
    performanceBucketMs: 600000,
    pageSize: 15,
    dashboardBlockCount: 20,
  },

  downloads: {
    version: 'v1.21.1',
    pagePath: '/download',
    baseUrl: '/downloads',
    files: [
      {
        id: 'windows-x64',
        os: 'Windows x64',
        icon: 'windows',
        file: 'NTMminer-windows-x64-v1.21.1.exe',
        status: 'ready',
        sha256: '9aae785569ebe2c2a469a0e93484b1e986fe86f0d6682284671c08f0a30f78f4',
        note: {
          zh: 'Windows 10/11 64 位。含 CPU + NVIDIA GPU 后端，原生多显卡（--gpu-devices）。含 Kadikama (rx/kad) 挖矿支持（CPU，RandomX v2 引擎）。★v1.21.1 是 Linux 端的 glibc 兼容性修复版，Windows 版除版本号外与 v1.21.0 完全相同，已在用的矿工无需重新下载。单文件静态构建，无需任何额外 DLL，解压即用。首次运行若被 Defender 拦截，选「仍要运行」。大页说明：首次以管理员身份运行一次，锄头会自动申请「锁定内存页」权限，注销重登录后即自动启用大页（日志出现 SeLockMemoryPrivilege enabled 即生效），RandomX 系算法算力显著提升；未授权时自动回退普通页，能挖但较慢。',
          en: 'Windows 10/11 64-bit. CPU + NVIDIA GPU backends, native multi-GPU (--gpu-devices). Includes Kadikama (rx/kad) CPU mining on the RandomX v2 engine. v1.21.1 is a Linux-side glibc compatibility fix; the Windows build is identical to v1.21.0 apart from the version string, so existing users do not need to re-download. Single static executable, no extra DLLs, no install needed. If Defender warns, choose “Run anyway”. Huge pages: run once as administrator and the miner requests the “Lock pages in memory” privilege automatically; after signing out and back in huge pages are enabled with no further setup (look for SeLockMemoryPrivilege enabled in the log), a large speedup on RandomX algorithms. Without the privilege it falls back to normal pages — still mines, just slower.',
        },
      },
      {
        id: 'linux-x64',
        os: 'Linux x64 / HiveOS',
        icon: 'linux',
        file: 'NTMminer-linux-x64-v1.21.1',
        status: 'ready',
        sha256: '811b829a673e275a530a617127e6db096ae4930b0c6ef58c71b1df403316491b',
        note: {
          zh: '通用 x86-64 Linux（Ubuntu 18.04+ / Debian 10+ / CentOS 7+ / HiveOS 等，只需 glibc 2.17 以上，几乎所有发行版都能直接跑）。★本版改用老基线构建，彻底解决 v1.21.0 及更早版本在 Ubuntu 22.04/20.04、HiveOS 上报「version `GLIBC_2.38\x27 not found / GLIBCXX_3.4.32 not found」无法启动的问题；算法与算力和 v1.21.0 完全一致（真机对拍打平）。含 CPU + NVIDIA GPU 后端，原生多显卡（--gpu-devices）。下载后 chmod +x 即可运行。大页说明：以 root 或 sudo 运行时，锄头会自动预留 2MiB 大页（无需任何手动配置），RandomX 系算法算力提升约 70%；若以普通用户运行且系统未预留大页，会自动回退普通页（能挖但明显变慢），此时可请管理员先执行 sudo sysctl -w vm.nr_hugepages=1400。',
          en: 'Generic x86-64 Linux (Ubuntu 18.04+, Debian 10+, CentOS 7+, HiveOS and friends — only glibc 2.17 or newer is required, so virtually any distribution works). This release is built against an old glibc baseline, fixing the "version `GLIBC_2.38\x27 not found / GLIBCXX_3.4.32 not found" startup failure that v1.21.0 and earlier hit on Ubuntu 22.04/20.04 and HiveOS. Algorithms and hashrate are identical to v1.21.0 (verified by back-to-back benchmarks on real hardware). CPU + NVIDIA GPU backends, native multi-GPU (--gpu-devices). chmod +x and run. Huge pages: when run as root (or via sudo) the miner reserves 2MiB huge pages automatically — no manual setup needed — worth roughly a 70% speedup on RandomX algorithms. Running as an unprivileged user without a pre-reserved pool falls back to normal pages (still mines, but noticeably slower); ask your admin to run sudo sysctl -w vm.nr_hugepages=1400 in that case.',
        },
      },
      {
        id: 'brva-windows-x64',
        os: 'Windows x64 — Brisvia (BRVA) 专用 / BRVA-only',
        icon: 'windows',
        file: 'NTMminer-brva-windows-x64-v1.20.0.exe',
        status: 'ready',
        sha256: 'fca118b83659d72b89fc332cb992b04acad3a158e3f3a3ec448c5e75ebfda707',
        note: {
          zh: '（备选）挖 Brisvia (BRVA / rx/brva) 的自家版本 —— 本池推荐官方锄头 xmrig-brisvia（见 BRVA 币页的接入命令），此版作为备选保留。上面的通用统一版不含该算法。Windows 10/11 64 位，纯 CPU（RandomX 抗 GPU，不提供 GPU 版）。单文件静态构建，无需任何额外 DLL。大页说明：首次以管理员身份运行一次，锄头会自动申请「锁定内存页」权限，注销重登录后自动启用大页（日志出现 SeLockMemoryPrivilege enabled 即生效），算力提升明显；未授权时自动回退普通页并打印黄色告警，能挖但较慢。用法示例：NTMminer-brva-windows-x64-v1.20.0.exe -a rx/brva -o hk2.ntmminer.com:5541 -u 你的brv1地址 --worker rig1',
          en: '(Alternative) Our own Brisvia (BRVA / rx/brva) build — this pool recommends the official xmrig-brisvia miner (see the connect command on the BRVA coin page); this build is kept as an alternative. The unified build above does not include this algorithm. Windows 10/11 64-bit, CPU only (RandomX is GPU-hostile, so no GPU build is offered). Single static executable, no extra DLLs. Huge pages: run once as administrator and the miner requests the "Lock pages in memory" privilege automatically; after signing out and back in huge pages engage (look for SeLockMemoryPrivilege enabled). Without it the miner falls back to normal pages with a warning — still mines, just slower. Example: NTMminer-brva-windows-x64-v1.20.0.exe -a rx/brva -o hk2.ntmminer.com:5541 -u YOUR_brv1_ADDRESS --worker rig1',
        },
      },
      {
        id: 'brva-linux-x64',
        os: 'Linux x64 / HiveOS — Brisvia (BRVA) 专用 / BRVA-only',
        icon: 'linux',
        file: 'NTMminer-brva-linux-x64-v1.20.0',
        status: 'ready',
        sha256: 'd25b7e84db7b106b8af32744b8c1f8adfcee2588d411f860ba79668307634183',
        note: {
          zh: '（备选）挖 Brisvia (BRVA / rx/brva) 的自家版本 —— 本池推荐官方锄头 xmrig-brisvia（见 BRVA 币页的接入命令），此版作为备选保留。上面的通用统一版不含该算法。通用 x86-64 Linux（Ubuntu 18.04+ / Debian 10+ / CentOS 7+ / HiveOS 等，只需 glibc 2.17 以上），老基线构建、C++ 运行时静态链入，不会出现 GLIBC/GLIBCXX 版本报错。纯 CPU（RandomX 抗 GPU，不提供 GPU 版）。下载后 chmod +x 即可运行。大页说明：以 root 或 sudo 运行会自动预留 2MiB 大页，RandomX 算力提升约 70%；普通用户且系统未预留时自动回退普通页并打印告警，可请管理员先执行 sudo sysctl -w vm.nr_hugepages=1400。用法示例：./NTMminer-brva-linux-x64-v1.20.0 -a rx/brva -o hk2.ntmminer.com:5541 -u 你的brv1地址 --worker rig1',
          en: '(Alternative) Our own Brisvia (BRVA / rx/brva) build — this pool recommends the official xmrig-brisvia miner (see the connect command on the BRVA coin page); this build is kept as an alternative. The unified build above does not include this algorithm. Generic x86-64 Linux (Ubuntu 18.04+, Debian 10+, CentOS 7+, HiveOS; only glibc 2.17 or newer required). Built against an old glibc baseline with the C++ runtime linked statically, so no GLIBC/GLIBCXX version errors. CPU only (RandomX is GPU-hostile, so no GPU build is offered). chmod +x and run. Huge pages: running as root (or via sudo) reserves 2MiB huge pages automatically, worth roughly 70% on RandomX; an unprivileged run without a pre-reserved pool falls back to normal pages with a warning — ask your admin to run sudo sysctl -w vm.nr_hugepages=1400. Example: ./NTMminer-brva-linux-x64-v1.20.0 -a rx/brva -o hk2.ntmminer.com:5541 -u YOUR_brv1_ADDRESS --worker rig1',
        },
      },
    ],
  },

  promotions: [
    {
      id: 'ourbit',
      enabled: true,
      placement: 'home',
      badge: { zh: '推广', en: 'Promotion' },
      title: { zh: 'Ourbit', en: 'Ourbit' },
      subtitle: { zh: 'ourbit.com', en: 'ourbit.com' },
      url: 'https://www.ourbit.com/register?inviteCode=2ZWA4P',
      icon: null,
    },
  ],

  coins: {
    brisvia: {
      id: 'brisvia',
      enabled: true,
      order: 5,
      symbol: 'BRVA',
      name: 'Brisvia',
      algo: 'rx/brva (RandomX)',
      color: '#2f7dd1',
      logo: null,
      apiBase: '/api-brva',
      hashUnit: 'H/s',
      amountDecimals: { min: 0, max: 4 },
      amountScale: 1,
      binaryUnits: null,
      stratum: [
        { region: 'HK2', host: 'hk2.ntmminer.com', port: 5541, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'HK2', host: 'hk2.ntmminer.com', port: 5542, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
        { region: 'HK3', host: 'hk3.ntmminer.com', port: 5541, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'HK3', host: 'hk3.ntmminer.com', port: 5542, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
        // 2026-08-02 新增三条 OVH 抗 DDoS 线路（德/新/加），端到端实测已通
        { region: 'SGP', host: 'sgp.ntmminer.com', port: 5541, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'SGP', host: 'sgp.ntmminer.com', port: 5542, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
        { region: 'EUR', host: 'eur.ntmminer.com', port: 5541, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'EUR', host: 'eur.ntmminer.com', port: 5542, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
        { region: 'CA', host: 'ca.ntmminer.com', port: 5541, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'CA', host: 'ca.ntmminer.com', port: 5542, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
      ],
      mining: {
        ntmAlgoFlag: 'rx/brva',
        // ★BRVA 主推官方 xmrig-brisvia（不主推自家 NTMminer）：命令框直接给官方锄头的命令行。
        // binaryOverride 只影响本币，其余币仍走 NTMminer 默认。
        binaryOverride: {
          windows: 'xmrig.exe',
          linux: './xmrig',
          algoFlag: 'rx/brva',
          extraArgs: ['-p x'],
        },
        // 通用 XMRig 挖不了 BRVA：算法虽是 stock rx/0，但 blob 是比特币 80 字节头、
        // nonce 在 offset 76，而通用 XMRig 的 rx/0 硬编码门罗布局 nonce@39 → 全部 Bad hash。
        // 必须用官方 fork xmrig-brisvia；xmrigCompatible 保持 false，避免打上"通用 XMRig 兼容"的误导标签。
        xmrigCompatible: false,
        xmrigAlgoFlag: null,
        modes: [
          {
            id: 'cpu',
            commandFlag: null,
            parameters: ['threads', 'smt', 'hugePages', 'noHugePages'],
            description: {
              zh: '★本池推荐官方锄头 xmrig-brisvia，下载：github.com/brisvia/xmrig-brisvia —— 下面的命令就是它的用法，复制即可用（命令行方式默认不开 TLS，无需再改配置文件；换成 hk3 等效，SOLO 请改用 5542 端口）。⚠ 通用版 XMRig 挖不了 BRVA：它按门罗布局把 nonce 写在 offset 39，而 BRVA 是比特币 80 字节头的 offset 76，必须用上面这个官方 fork。RandomX（rx/brva）只有 CPU 后端 —— RandomX 是刻意抗 GPU 的访存密集算法，实测同代 GPU 比 CPU 慢一个数量级，因此没有 GPU 版；不要添加 --gpu、--gpu-only 或 --gpu-devices。（我们自家的 NTMminer BRVA 专用版仍在下载页保留，作为备选，把命令里的 xmrig 换成对应文件名即可。）',
              en: 'Recommended miner for this pool: the official xmrig-brisvia (github.com/brisvia/xmrig-brisvia). The command below is exactly how to run it — copy and go (the command line form does not enable TLS, so no config file edit is needed; hk3 works the same way, and SOLO uses port 5542). Note: stock XMRig cannot mine BRVA — it writes the nonce at offset 39 per the Monero layout, while BRVA uses a Bitcoin 80-byte header with the nonce at offset 76, so the official fork above is required. RandomX (rx/brva) is CPU-only: it is deliberately memory-hard and GPU-hostile, and on real hardware a same-generation GPU is an order of magnitude slower than the CPU, so there is no GPU build — do not add --gpu, --gpu-only, or --gpu-devices. (Our own NTMminer BRVA build remains on the download page as an alternative; just swap xmrig for that filename.)',
            },
          },
        ],
      },
      wallet: {
        addressPrefixes: ['brv1'],
        example: {
          zh: 'brv1q...（你的 Brisvia 主网地址）',
          en: 'brv1q... (your Brisvia mainnet address)',
        },
      },
      settlement: {
        confirmations: 100,
        directPayout: false,
        noPayout: false,
        notice: {
          zh: '⛏ 主网 2026-08-01 15:00 UTC 开网，公平启动、0 预挖、0 开发者税。开网前矿池会回复「暂无任务」，属正常现象 —— 节点已就位，链一开自动派发任务。coinbase 需 100 个确认才成熟，首批打款约在开网 3～4 小时后。',
          en: 'Mainnet opens 2026-08-01 15:00 UTC — fair launch, no premine, no dev tax. Before that the pool answers "no job available"; this is expected — the node is already in place and jobs start flowing the moment the chain opens. Coinbase outputs need 100 confirmations to mature, so the first payouts land roughly 3-4 hours after launch.',
        },
      },
      links: {
        site: 'https://brisvia.com',
        git: 'https://github.com/brisvia/brisvia',
      },
      description: {
        zh: 'Brisvia（BRVA）是 Bitcoin Core v30.2 的 fork：把 SHA256d 换成未魔改的 RandomX（rx/brva），难度用逐块 ASERT。公平启动、0 预挖、0 开发者税，50 BRVA/块、约 120 秒一块、100 万块减半、上限 1 亿。本池采用 PPLNS，费率 1%，起领额 0.1 BRVA，coinbase 满 100 确认后结算。挖矿推荐官方锄头 xmrig-brisvia（github.com/brisvia/xmrig-brisvia），命令行一条即可接入：./xmrig -a rx/brva -o hk2.ntmminer.com:5541 -u YOUR_BRVA_WALLET -p x（hk3 等效，SOLO 用 5542；命令行方式默认不开 TLS，不必再改配置文件）。我们自家的 NTMminer BRVA 专用版仍在下载页保留，作为备选。注意通用版 XMRig 挖不了 BRVA —— 它的 rx/0 按门罗布局把 nonce 写在 offset 39，而 BRVA 是比特币 80 字节头的 offset 76。',
        en: 'Brisvia (BRVA) is a Bitcoin Core v30.2 fork that replaces SHA256d with stock, unmodified RandomX (rx/brva) and retargets every block with ASERT. Fair launch: no premine, no dev tax, 50 BRVA per block, ~120s block time, halving every 1,000,000 blocks, 100M cap. This pool is PPLNS with a 1% fee and a 0.1 BRVA minimum payout; coinbase matures at 100 confirmations. Recommended miner: the official xmrig-brisvia (github.com/brisvia/xmrig-brisvia) — a single command line is enough: ./xmrig -a rx/brva -o hk2.ntmminer.com:5541 -u YOUR_BRVA_WALLET -p x (hk3 is equivalent, SOLO uses 5542; the command line form does not enable TLS, so no config file edit is needed). Our own NTMminer BRVA build stays on the download page as an alternative. Note that stock XMRig cannot mine BRVA — its rx/0 writes the nonce at offset 39 per the Monero layout, while BRVA uses a Bitcoin 80-byte header with the nonce at offset 76.',
      },
    },

    juno: {
      id: 'juno',
      enabled: true,
      order: 8,
      symbol: 'JUNO',
      name: 'Juno Cash',
      algo: 'rx/juno (RandomX)',
      color: '#d9a62e',
      logo: null,
      apiBase: '/api-juno',
      hashUnit: 'H/s',
      amountDecimals: { min: 0, max: 4 },
      amountScale: 1,
      binaryUnits: null,
      stratum: [
        { region: 'HK', host: 'hk.ntmminer.com', port: 5561, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'HK', host: 'hk.ntmminer.com', port: 5562, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
        { region: 'HK3', host: 'hk3.ntmminer.com', port: 5561, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'HK3', host: 'hk3.ntmminer.com', port: 5562, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
        { region: 'SGP', host: 'sgp.ntmminer.com', port: 5561, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'SGP', host: 'sgp.ntmminer.com', port: 5562, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
        { region: 'DE', host: 'eur.ntmminer.com', port: 5561, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'DE', host: 'eur.ntmminer.com', port: 5562, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
        { region: 'CA', host: 'ca.ntmminer.com', port: 5561, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'CA', host: 'ca.ntmminer.com', port: 5562, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
        { region: 'US', host: 'us.ntmminer.com', port: 5561, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'US', host: 'us.ntmminer.com', port: 5562, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
      ],
      mining: {
        ntmAlgoFlag: 'rx/juno',
        // ★JUNO 主推官方锄头 junorig（xmrig 的 Juno 官方 fork）：命令框直接给它的命令行。
        binaryOverride: {
          windows: 'junorig.exe',
          linux: './junorig',
          algoFlag: 'rx/juno',
          extraArgs: [],
        },
        // 通用 XMRig 挖不了 JUNO：rx/juno 是 Zcash 式 140 字节头 + 32 字节 nonce@108，
        // 通用 XMRig 没有该算法/布局，必须用官方 fork junorig。
        xmrigCompatible: false,
        xmrigAlgoFlag: null,
        modes: [
          {
            id: 'cpu',
            commandFlag: null,
            parameters: ['threads'],
            description: {
              zh: '★本池推荐官方锄头 junorig，下载：github.com/juno-cash/junorig/releases —— 下面的命令就是它的用法，复制即可用（-u 填你的 j1 统一地址；换 hk3/sgp/eur/ca/us 等效，SOLO 请改用 5562 端口；-t 可指定线程数，默认全核）。⚠ 通用版 XMRig 没有 rx/juno 算法，挖不了 JUNO，必须用官方 fork junorig。RandomX 只有 CPU 后端 —— 刻意抗 GPU 的访存密集算法，实测同代 GPU 比 CPU 慢一个数量级，因此没有 GPU 版。RandomX fast 模式约需 2.3 GB 空闲内存。',
              en: 'Recommended miner: the official junorig (github.com/juno-cash/junorig/releases). The command below is exactly how to run it — copy and go (-u takes your j1 unified address; hk3/sgp/eur/ca/us work the same, SOLO uses port 5562; -t sets thread count, default all cores). Note: stock XMRig does not include the rx/juno algorithm and cannot mine JUNO — the official junorig fork is required. RandomX is CPU-only: deliberately memory-hard and GPU-hostile (a same-generation GPU is an order of magnitude slower), so there is no GPU build. RandomX fast mode needs about 2.3 GB of free RAM.',
            },
          },
        ],
      },
      wallet: {
        addressPrefixes: ['j1'],
        example: {
          zh: 'j1...（你的 Juno 统一地址；junocashd 里 z_getnewaccount + z_getaddressforaccount 生成）',
          en: 'j1... (your Juno unified address; create one with z_getnewaccount + z_getaddressforaccount in junocashd)',
        },
      },
      settlement: {
        confirmations: 110,
        directPayout: false,
        noPayout: false,
        notice: {
          zh: '🛡 JUNO 是全隐蔽链：coinbase 必须先进入 Orchard 隐蔽池才能花费，因此结算确认数为 110（100 个成熟确认 + 转入隐蔽池的余量）。打款为 Orchard→Orchard 全隐蔽交易，直接付到你的 j1 统一地址，链上不暴露金额与收款方。',
          en: 'JUNO is a fully-shielded chain: coinbase must enter the Orchard shielded pool before it can be spent, so settlement uses 110 confirmations (100 for maturity plus headroom for shielding). Payouts are fully-shielded Orchard-to-Orchard transactions straight to your j1 unified address — amounts and recipients are not visible on-chain.',
        },
      },
      links: {
        site: 'https://juno.cash',
        git: 'https://github.com/juno-cash/junocash',
      },
      description: {
        zh: 'Juno Cash（JUNO）是 Zcash 6.1 的 fork：把 Equihash 换成标准参数的 RandomX（rx/juno），100% 流通量由零知识证明保护 —— 挖矿用透明地址仅为审计发行量，任何新币必须先进入 Orchard 隐蔽池才能流通。公平启动、0 预挖、0 开发者税，约 60 秒一块、当前 6.25 JUNO/块、上限 2100 万。本池 PPLNS 费率 1%，SOLO 费率 1%，起领额 0.1 JUNO，结算 110 确认；打款为全隐蔽 Orchard 交易直达你的 j1 地址。挖矿用官方锄头 junorig（github.com/juno-cash/junorig），一条命令接入：./junorig -a rx/juno -o hk.ntmminer.com:5561 -u 你的j1地址（SOLO 用 5562；hk3/sgp/eur/ca/us 等效）。',
        en: 'Juno Cash (JUNO) is a Zcash 6.1 fork that replaces Equihash with stock-parameter RandomX (rx/juno). 100% of the circulating supply is protected by zero-knowledge proofs — mining uses transparent addresses only to keep issuance auditable, and every new coin must enter the Orchard shielded pool before it can circulate. Fair launch: no premine, no dev tax, ~60-second blocks, currently 6.25 JUNO per block, 21M cap. This pool is PPLNS at a 1% fee (SOLO also 1%) with a 0.1 JUNO minimum payout and 110-confirmation settlement; payouts are fully-shielded Orchard transactions straight to your j1 unified address. Mine with the official junorig (github.com/juno-cash/junorig) — one command: ./junorig -a rx/juno -o hk.ntmminer.com:5561 -u YOUR_j1_ADDRESS (SOLO on 5562; hk3/sgp/eur/ca/us are equivalent).',
      },
    },

    dragonx: {
      id: 'dragonx',
      enabled: true,
      order: 10,
      symbol: 'DRGX',
      name: 'DragonX',
      algo: 'rx/dragonx',
      color: '#e34948',
      logo: '/img/dragonx.png',
      apiBase: '/api-dragonx',
      hashUnit: 'H/s',
      amountDecimals: { min: 0, max: 4 },
      amountScale: 1,
      binaryUnits: null,
      stratum: [
        { region: 'HK', host: 'hk.ntmminer.com', port: 3333, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'HK', host: 'hk.ntmminer.com', port: 7777, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
        { region: 'HK2', host: 'hk2.ntmminer.com', port: 3333, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'HK2', host: 'hk2.ntmminer.com', port: 7777, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
        { region: 'HK3', host: 'hk3.ntmminer.com', port: 3333, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'HK3', host: 'hk3.ntmminer.com', port: 7777, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
        { region: 'SG', host: 'sgp.ntmminer.com', port: 3333, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'SG', host: 'sgp.ntmminer.com', port: 7777, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
        { region: 'DE', host: 'eur.ntmminer.com', port: 3333, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'DE', host: 'eur.ntmminer.com', port: 7777, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
        { region: 'CA', host: 'ca.ntmminer.com', port: 3333, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'CA', host: 'ca.ntmminer.com', port: 7777, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
        { region: 'US', host: 'us.ntmminer.com', port: 3333, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'US', host: 'us.ntmminer.com', port: 7777, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
      ],
      mining: {
        ntmAlgoFlag: 'rx/dragonx',
        xmrigCompatible: true,
        xmrigAlgoFlag: 'rx/dragonx',
        modes: [
          {
            id: 'cpu',
            commandFlag: null,
            parameters: ['threads', 'smt', 'hugePages', 'noHugePages', 'msr'],
            description: {
              zh: 'RandomX（rx/dragonx）仅提供 CPU 后端；不要添加 --gpu、--gpu-only 或 --gpu-devices。',
              en: 'RandomX (rx/dragonx) has a CPU backend only; do not add --gpu, --gpu-only, or --gpu-devices.',
            },
          },
        ],
      },
      wallet: {
        addressPrefixes: ['zs1'],
        example: {
          zh: 'zs1qknw...（你的 Sapling zs 地址）',
          en: 'zs1qknw... (your Sapling zs address)',
        },
      },
      settlement: {
        confirmations: 10,
        directPayout: false,
        noPayout: false,
        notice: null,
      },
      links: {
        site: 'https://dragonx.is',
        git: 'https://git.dragonx.is/DragonX/dragonx',
      },
      description: {
        zh: 'DragonX（DRGX）是 Hush / Komodo 系的强隐私链，PoW 为 RandomX 变体 rx/dragonx —— CPU 友好、抗 ASIC。矿工登录与收款均使用 zs 隐私地址。',
        en: 'DragonX (DRGX) is a privacy chain in the Hush / Komodo family. PoW is the rx/dragonx RandomX variant — CPU friendly, ASIC resistant. Miners use shielded zs addresses for both login and payouts.',
      },
    },

    /* ⛔ 2026-07-31 下线：DOM 矿池已停（老机 07-24 到期，链数据未迁，复活需重新同步）。
       前端是 Object.entries(CFG.coins) 全量遍历，不看 enabled/order 字段，所以下线只能
       把整块移出 coins —— 这里用块注释保留完整配置，将来复活把注释去掉即可。
    dom: {
      id: 'dom',
      enabled: true,
      order: 15,
      symbol: 'DOM',
      name: 'DOM',
      algo: 'rx/dom (RandomX)',
      color: '#22c1a4',
      logo: null,
      apiBase: '/api-dom',
      hashUnit: 'H/s',
      amountDecimals: { min: 0, max: 4 },
      amountScale: 1,
      binaryUnits: null,
      stratum: [
        { region: 'HK', host: 'hk.ntmminer.com', port: 5533, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
      ],
      mining: {
        ntmAlgoFlag: 'rx/dom',
        xmrigCompatible: false,
        xmrigAlgoFlag: null,
        modes: [
          {
            id: 'cpu',
            commandFlag: null,
            parameters: ['threads', 'smt', 'hugePages', 'noHugePages', 'msr'],
            description: {
              zh: 'DOM 是 RandomX 币，只能用 CPU 挖（RandomX 刻意抗 GPU，显卡挖它比 CPU 慢一个数量级）。默认自动开 2 MiB 大页，fast 模式需约 2.3 GB 空闲内存；内存不足时加 --no-huge-pages。',
              en: 'DOM is a RandomX coin and is CPU-only (RandomX is deliberately GPU-hostile — a GPU is an order of magnitude slower than a CPU here). 2 MiB huge pages are enabled automatically; fast mode needs about 2.3 GB of free RAM. Add --no-huge-pages if memory is tight.',
            },
          },
        ],
      },
      wallet: {
        addressPrefixes: ['domh_'],
        example: {
          zh: 'domh_ + 64 位小写十六进制（共 69 个字符）。这是公开 mining ID，不是钱包地址，也不是秘密领取凭证。请在“账户与领取”页由浏览器本机生成；锄头的 -u 只能填写 domh_。',
          en: 'domh_ plus 64 lowercase hexadecimal characters (69 characters total). This is a public mining ID, not a wallet address or secret claim credential. Generate it locally on the Account & claims tab; only domh_ belongs in NTMminer -u.',
        },
      },
      settlement: {
        confirmations: 1001,
        directPayout: false,
        noPayout: false,
        payoutPaused: false,
        notice: null,
      },
      account: {
        type: 'dom-slate-v4',
        enabled: true,
        apiBase: '/api-dom',
        tutorialImage: '/img/dom-slate-tutorial-cn.20260720.png',
        registrationEnabled: true,
        rotationEnabled: false,
        claimsEnabled: true,
        paymentHistoryEnabled: false,
        notice: {
          zh: '领取与新账户绑定已开放；早期测试阶段每个账户每批最多领取 3 DOM。凭证轮换仍关闭。',
          en: 'Claims and new-account binding are open. During the early test phase, each account can claim up to 3 DOM per batch. Credential rotation remains disabled.',
        },
      },
      links: {
        site: 'https://github.com/sorenplanck/dom-protocol',
        git: 'https://github.com/sorenplanck/dom-protocol',
      },
      description: {
        zh: 'DOM 是采用 Mimblewimble 的隐私链，PoW 为 RandomX（rx/dom）—— CPU 挖矿、抗 ASIC。主网 2026-07 启动，出块约 120 秒、初始奖励 33 DOM；coinbase 需要其后再产生 1000 个区块才成熟，矿池参数因此写为 1001。本池采用 PPLNS，费率 5%，起领额 3 DOM。DOM 没有供矿池单边打款的传统固定地址：挖矿时 -u 填公开 domh_；领取时用秘密 domw_ 打开账户，再由 Wallet V3 对每个 DOMSLATE4 request 生成 response。早期测试阶段每批最多领取 3 DOM。',
        en: 'DOM is a Mimblewimble privacy chain using RandomX (rx/dom), designed for CPU mining and ASIC resistance. Mainnet launched in July 2026 with roughly 120-second blocks and a 33 DOM initial reward. A coinbase output becomes mature only after 1000 subsequent blocks, so the pool setting is 1001. This pool uses PPLNS with a 5% fee and a 3 DOM claim minimum. DOM has no conventional fixed address for unilateral pool payments: mine with public domh_, open the account with secret domw_, then use Wallet V3 to create a response for each DOMSLATE4 request. Early test batches are capped at 3 DOM.',
      },
    },
    */

    btc09: {
      id: 'btc09',
      enabled: true,
      order: 20,
      symbol: '09C',
      name: 'Bitcoin 09',
      algo: 'Argon2id (64 MiB)',
      color: '#f7931a',
      logo: '/img/bitcoin09.png',
      apiBase: '/api-btc09',
      hashUnit: 'H/s',
      amountDecimals: { min: 0, max: 4 },
      amountScale: 1,
      binaryUnits: null,
      stratum: [
        { region: 'HK2', host: 'hk2.ntmminer.com', port: 8344, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'HK2', host: 'hk2.ntmminer.com', port: 8345, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
        { region: 'HK3', host: 'hk3.ntmminer.com', port: 8344, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'HK3', host: 'hk3.ntmminer.com', port: 8345, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
        // 2026-08-02 新增三条 OVH 抗 DDoS 线路（德/新/加），端到端实测已通
        { region: 'SGP', host: 'sgp.ntmminer.com', port: 8344, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'SGP', host: 'sgp.ntmminer.com', port: 8345, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
        { region: 'EUR', host: 'eur.ntmminer.com', port: 8344, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'EUR', host: 'eur.ntmminer.com', port: 8345, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
        { region: 'CA', host: 'ca.ntmminer.com', port: 8344, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'CA', host: 'ca.ntmminer.com', port: 8345, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
      ],
      mining: {
        ntmAlgoFlag: 'btc09',
        xmrigCompatible: false,
        xmrigAlgoFlag: null,
        modes: [
          {
            id: 'cpu',
            commandFlag: null,
            parameters: ['threads', 'smt', 'hugePages', 'noHugePages'],
            description: {
              zh: '不传 GPU 参数时启动 CPU Argon2id worker；每个线程使用 64 MiB 内存。',
              en: 'With no GPU option, CPU Argon2id workers start; each thread uses 64 MiB of memory.',
            },
          },
          {
            id: 'gpu',
            commandFlag: '--gpu-only',
            parameters: ['gpuOnly', 'gpuDevices'],
            description: {
              zh: '--gpu-only 隐含 --gpu；btc09 的 CUDA 路径为每张选中显卡启动一个 GPU worker，不启动 CPU worker。',
              en: '--gpu-only implies --gpu; the btc09 CUDA path starts one GPU worker per selected card and does not start CPU workers.',
            },
          },
        ],
      },
      wallet: {
        addressPrefixes: ['4'],
        example: { zh: '4k26Vj...（你的 09C 地址）', en: '4k26Vj... (your 09C address)' },
      },
      settlement: { confirmations: 10, directPayout: false, noPayout: false, notice: null },
      links: {
        site: 'https://btc09.org',
        git: 'https://github.com/krutftw/bitcoin09',
      },
      description: {
        zh: 'Bitcoin 09（09C）是 Bitcoin 的 clean-room Go 重写，只改一处：PoW 换成 64 MiB Argon2id —— 内存硬。CPU 可挖，NTMminer v1.17.0 起也支持 NVIDIA GPU：64 MiB / t=1 属带宽 bound、GPU 友好（单卡算力约等于数十个 CPU 核；并行度受显存限制，每个 hash 占 64 MiB）。2100 万上限、50 币补贴、10 分钟出块、每 21 万块减半，经济模型与比特币逐条一致。',
        en: 'Bitcoin 09 (09C) is a clean-room Go rewrite of Bitcoin with one change: PoW is Argon2id at 64 MiB per hash — memory-hard. Mine it on CPU or, from NTMminer v1.17.0, on NVIDIA GPU: the 64 MiB / t=1 parameters are bandwidth-bound and GPU-friendly (a single card ≈ dozens of CPU cores; concurrency is capped by VRAM, 64 MiB per hash). 21M cap, 50-coin subsidy, 10-minute blocks, halving every 210,000 blocks.',
      },
    },

    /* ⛔ 2026-07-31 下线：Noctari(NCTI) 与 Velkar(VELK) 矿池均已停
       —— NCTI 的 PoW 期已结束转 PoS；VELK 老机 07-24 到期全停未迁（且该池被承包不打款）。
       同上：前端全量遍历 coins，只能整块移出；块注释保留配置，复活去掉注释即可。
    noctari: {
      id: 'noctari',
      enabled: true,
      order: 30,
      symbol: 'NCTI',
      name: 'Noctari',
      algo: 'Quark',
      color: '#8b7cf6',
      logo: '/img/noctari.png',
      apiBase: '/api-noctari',
      hashUnit: 'H/s',
      amountDecimals: { min: 0, max: 4 },
      amountScale: 1,
      binaryUnits: null,
      stratum: [
        { region: 'HK', host: 'hk.ntmminer.com', port: 4455, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'HK', host: 'hk.ntmminer.com', port: 4456, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
      ],
      mining: {
        ntmAlgoFlag: 'quark',
        xmrigCompatible: false,
        xmrigAlgoFlag: null,
        modes: [
          {
            id: 'cpu',
            commandFlag: null,
            parameters: ['threads', 'smt'],
            description: {
              zh: '不传 GPU 参数时只启动 CPU worker。Quark 是纯整数哈希、无大内存，huge pages 不适用。',
              en: 'With no GPU option, only CPU workers start. Quark is an integer-only hash without a large-memory stage, so huge pages do not apply.',
            },
          },
          {
            id: 'gpu',
            commandFlag: '--gpu-only',
            parameters: ['gpuOnly', 'gpuDevices'],
            description: {
              zh: '--gpu-only 隐含 --gpu；只启动选中显卡的 GPU worker，不启动 CPU worker。',
              en: '--gpu-only implies --gpu; only GPU workers for the selected cards start, with no CPU workers.',
            },
          },
          {
            id: 'hybrid',
            commandFlag: '--gpu',
            parameters: ['gpu', 'gpuDevices'],
            description: {
              zh: '--gpu 同时启动 CPU worker 与每张选中显卡的 GPU worker，实现 CPU + GPU 同挖。',
              en: '--gpu starts CPU workers together with one GPU worker per selected card for simultaneous CPU + GPU mining.',
            },
          },
        ],
      },
      wallet: {
        addressPrefixes: ['N'],
        example: { zh: 'Ngar...（你的 NCTI 地址）', en: 'Ngar... (your NCTI address)' },
      },
      settlement: { confirmations: 100, directPayout: false, noPayout: false, notice: null },
      links: {
        site: 'https://github.com/noctari-core/noctari',
        git: 'https://github.com/noctari-core/noctari',
      },
      description: {
        zh: 'Noctari（NCTI）是 PIVX v5 fork，用 Quark 算法（blake/bmw/groestl/jh/keccak/skein 九轮链式哈希）挖矿 —— CPU、GPU 均可。公平启动：区块 1–20159 为约 7 天的 PoW 窗口（50 NCTI/块全归矿工、零 premine），窗口结束后永久转 PoS。本池 PPLNS 结算：爆块奖励先进矿池钱包，coinbase 满 100 确认成熟后按 share 占比打款给矿工。',
        en: 'Noctari (NCTI) is a PIVX v5 fork mined with the Quark algorithm (a 9-round hash chain of blake/bmw/groestl/jh/keccak/skein) — CPU and GPU friendly. Fair launch: blocks 1–20159 are a ~7-day PoW window (50 NCTI per block, entirely to miners, zero premine) after which the chain switches to PoS permanently. This pool settles via PPLNS: block rewards accrue to the pool wallet and are paid out by share weight once coinbase matures at 100 confirmations.',
      },
    },

    velkar: {
      id: 'velkar',
      enabled: true,
      order: 40,
      symbol: 'VELK',
      name: 'Velkar',
      algo: 'VelkarHash',
      color: '#6366f1',
      logo: null,
      apiBase: '/api-velkar',
      hashUnit: 'H/s',
      amountDecimals: { min: 0, max: 4 },
      amountScale: 1,
      binaryUnits: null,
      stratum: [
        { region: 'HK', host: 'hk.ntmminer.com', port: 5511, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'HK', host: 'hk.ntmminer.com', port: 5512, mode: 'solo', label: { zh: 'SOLO', en: 'SOLO' }, tls: false },
      ],
      mining: {
        ntmAlgoFlag: 'velkarhash',
        xmrigCompatible: false,
        xmrigAlgoFlag: null,
        modes: [
          {
            id: 'cpu',
            commandFlag: null,
            parameters: ['threads', 'smt'],
            description: {
              zh: '不传 GPU 参数时启动 VelkarHash CPU worker。',
              en: 'With no GPU option, VelkarHash CPU workers start.',
            },
          },
          {
            id: 'gpu',
            commandFlag: '--gpu-only',
            parameters: ['gpuOnly', 'gpuDevices'],
            description: {
              zh: '--gpu-only 隐含 --gpu；VelkarHash 的 CUDA 路径为每张选中显卡启动一个 GPU worker，不启动 CPU worker。',
              en: '--gpu-only implies --gpu; the VelkarHash CUDA path starts one GPU worker per selected card and does not start CPU workers.',
            },
          },
        ],
      },
      wallet: {
        addressPrefixes: ['velkar:'],
        example: { zh: 'velkar:qz...（你的 VELK 地址）', en: 'velkar:qz... (your VELK address)' },
      },
      settlement: {
        confirmations: 30,
        directPayout: false,
        noPayout: true,
        notice: {
          zh: '⚠ 本矿池不打款：VELK 矿池已被大佬整体承包用于测试，所有爆块收益归承包方，不向矿工发放。请知悉后再决定是否接入。',
          en: '⚠ NO PAYOUTS: this VELK pool is fully contracted by a private sponsor for testing. All block rewards go to the sponsor — nothing is paid out to miners. Please mine here only if you understand this.',
        },
      },
      links: {
        site: 'https://github.com/VelkarVELK',
        git: 'https://github.com/VelkarVELK/velkar-wallet',
      },
      description: {
        zh: 'Velkar（VELK）是 Kaspa（rusty-kaspa）BlockDAG fork，PoW 为魔改 VelkarHash：keccak-f1600 → 64×64 矩阵 heavy-hash → Argon2id 8 MiB 内存硬阶段。多出的阶段故意与通用 Kaspa 锄头不兼容 —— 唯一支持的锄头是 NTMminer（CPU 可挖，v1.19.0 起支持 NVIDIA GPU）。链已于 2026-07-15 从新 genesis 重启。注意：本池被承包用于测试，不打款。',
        en: 'Velkar (VELK) is a Kaspa (rusty-kaspa) BlockDAG fork with a custom VelkarHash PoW: keccak-f1600 → a 64×64 matrix heavy-hash → an Argon2id 8 MiB memory-hard stage. The extra stage is deliberately incompatible with generic Kaspa miners — NTMminer is the only supported miner (CPU, and NVIDIA GPU from v1.19.0). The chain relaunched from a new genesis on 2026-07-15. Note: this pool is contracted for testing and does NOT pay out.',
      },
    },
    */
  },
};
