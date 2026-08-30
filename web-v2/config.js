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
    // ★ 2026-08-30 起：下载页 = 锄头卡片（/download）→ 锄头详情页（/download/<id>）。
    // 每个锄头一个条目：files=可下载文件、quick=一键命令生成器、cli=完整参数表（事实源=实跑 --help）、highlights/faq/changelog=说明。
    // 币页 connect 命令用哪个锄头由 coins.<id>.mining.miner 指向这里的 id（file.run = 下载/解压后要执行的文件名）。
    // 抽水属对外承诺：devFeePercent / 锄头启动横幅 / 程序内参数三者必须一致，改任一处同步另两处。
    pagePath: '/download',
    baseUrl: '/downloads',
    miners: [
      {
        id: 'ntmminer',
        name: 'NTMminer',
        kind: 'universal',
        version: 'v1.21.1',
        released: '2026-07-25',
        color: '#2563eb',
        logo: '/img/ntm-mark.svg',
        symbol: 'NTM',
        devFeePercent: 0,
        feeAddress: null,
        hardware: ['cpu', 'gpu'],
        tagline: {
          zh: '通用多算法锄头：一个文件覆盖 16 种算法，0% 抽水',
          en: 'The universal multi-algorithm miner: one file, 16 algorithms, 0% dev fee',
        },
        summary: {
          zh: 'CPU + NVIDIA GPU 同一个二进制。本站有矿池的币里 DragonX 用它挖；其余算法可接第三方矿池。体积约 30 MB。',
          en: 'CPU + NVIDIA GPU in one binary. Among the pools on this site it mines DragonX; the other algorithms connect to third-party pools. About 30 MB.',
        },
        algos: ['neuromorph', 'argon2id-blocknet', 'midstate', 'rx/dragonx', 'zoka', 'rx/brva', 'rx/tar', 'rx/zeph', 'rx/dom', 'rx/scash', 'rx/kad', 'btx', 'qpow', 'btc09', 'quark', 'velkarhash'],
        requirements: {
          zh: 'Windows 10/11 64 位，或任意 x86-64 Linux（Ubuntu 18.04+ / Debian 10+ / CentOS 7+ / HiveOS，只需 glibc 2.17 以上）。GPU 算法（midstate / btx / qpow）需要 NVIDIA 驱动，不用装 CUDA。RandomX 系算法（rx/*、zoka）只用 CPU，GPU 不参与。',
          en: 'Windows 10/11 64-bit, or any x86-64 Linux (Ubuntu 18.04+, Debian 10+, CentOS 7+, HiveOS; only glibc 2.17 or newer). GPU algorithms (midstate / btx / qpow) need an NVIDIA driver, no CUDA install. RandomX-family algorithms (rx/*, zoka) run on the CPU only.',
        },
        files: [
          {
            id: 'windows-x64',
            os: 'Windows x64',
            icon: 'windows',
            file: 'NTMminer-windows-x64-v1.21.1.exe',
            run: 'NTMminer-windows-x64-v1.21.1.exe',
            status: 'ready',
            size: 32485377,
            sha256: '9aae785569ebe2c2a469a0e93484b1e986fe86f0d6682284671c08f0a30f78f4',
            note: {
              zh: '单文件静态构建，不需要任何额外 DLL，下载即用。首次运行若被 Defender 拦截，选「仍要运行」。v1.21.1 的 Windows 版除版本号外与 v1.21.0 完全相同。',
              en: 'Single static executable, no extra DLLs, run as downloaded. If Defender warns on first run, choose "Run anyway". The Windows build of v1.21.1 is identical to v1.21.0 apart from the version string.',
            },
          },
          {
            id: 'linux-x64',
            os: 'Linux x64 / HiveOS',
            icon: 'linux',
            file: 'NTMminer-linux-x64-v1.21.1',
            run: './NTMminer-linux-x64-v1.21.1',
            status: 'ready',
            size: 30807952,
            sha256: '811b829a673e275a530a617127e6db096ae4930b0c6ef58c71b1df403316491b',
            note: {
              zh: '通用 x86-64 Linux，老基线构建（glibc 2.17 以上即可），彻底解决旧版在 Ubuntu 22.04/20.04、HiveOS 上报 GLIBC_2.38 / GLIBCXX 版本错的问题。下载后 chmod +x 即可运行。',
              en: 'Generic x86-64 Linux built against an old baseline (glibc 2.17+), fixing the GLIBC_2.38 / GLIBCXX version errors older builds hit on Ubuntu 22.04/20.04 and HiveOS. chmod +x and run.',
            },
          },
        ],
        quick: {
          workerDefault: 'rig1',
          intro: {
            zh: '本站有矿池的币直接选；挖其它币选「其它矿池」自己填地址。填好收款地址后，下面四个框会同步生成命令。',
            en: 'Pick a coin that has a pool on this site, or choose "Other pool" and type the pool address yourself. Fill in your payout address and the four boxes below update together.',
          },
          custom: {
            algos: ['neuromorph', 'argon2id-blocknet', 'midstate', 'rx/dragonx', 'zoka', 'rx/brva', 'rx/tar', 'rx/zeph', 'rx/dom', 'rx/scash', 'rx/kad', 'btx', 'qpow', 'btc09', 'quark', 'velkarhash'],
            template: '{bin} -a {algo} -o {pool} -u {address} --worker {worker}',
            note: {
              zh: '这里生成的是纯 CPU 命令。GPU 算法（midstate / btx / qpow）加 --gpu-only（纯 GPU）或 --gpu，见下方完整参数。',
              en: 'This generates the CPU-only command. For GPU algorithms (midstate / btx / qpow) add --gpu-only (GPU only) or --gpu, see the full reference below.',
            },
          },
          targets: [],
        },
        highlights: [
          {
            title: { zh: 'Windows 大页：跑一次管理员即可', en: 'Windows huge pages: run once as administrator' },
            body: {
              zh: '首次以管理员身份运行一次，锄头会自动申请「锁定内存页」权限，注销重登录后自动启用大页（日志出现 SeLockMemoryPrivilege enabled 即生效），RandomX 系算法算力显著提升；未授权时自动回退普通页，能挖但较慢。',
              en: 'Run once as administrator and the miner requests the "Lock pages in memory" privilege automatically; after signing out and back in huge pages are enabled with no further setup (look for SeLockMemoryPrivilege enabled in the log) - a large speedup on RandomX algorithms. Without it the miner falls back to normal pages, still mining but slower.',
            },
          },
          {
            title: { zh: 'Linux 大页：root 自动预留', en: 'Linux huge pages: reserved automatically as root' },
            body: {
              zh: '以 root 或 sudo 运行时锄头自动预留 2MiB 大页（无需手动配置），RandomX 系算法提升约 70%。以普通用户运行且系统未预留大页时自动回退普通页（能挖但明显变慢），此时可请管理员先执行 sudo sysctl -w vm.nr_hugepages=1400。',
              en: 'When run as root (or via sudo) the miner reserves 2MiB huge pages itself (no manual setup), worth roughly 70% on RandomX algorithms. Running as an unprivileged user without a pre-reserved pool falls back to normal pages (still mines, noticeably slower); ask your admin to run sudo sysctl -w vm.nr_hugepages=1400 in that case.',
            },
          },
          {
            title: { zh: '多显卡原生支持', en: 'Native multi-GPU' },
            body: {
              zh: 'v1.14.0 起一进程驱动全部 NVIDIA 卡：--gpu-devices 0,1,3 指定用哪几张，不写默认用全部卡；越界的编号自动忽略。想每卡单独看算力仍可每卡开一个进程。',
              en: 'Since v1.14.0 one process drives every NVIDIA card: --gpu-devices 0,1,3 picks specific cards, omit it to use all of them; out-of-range indices are ignored. Run one process per card if you want per-card hashrate.',
            },
          },
        ],
        faq: [
          {
            q: { zh: 'Defender / 杀毒软件报毒怎么办？', en: 'Defender or my antivirus flags the file - what now?' },
            a: {
              zh: '闭源锄头常被启发式误报。先核对 SHA-256 与本页一致，再在 Defender 里选「仍要运行」或把该文件加入排除项。',
              en: 'Closed-source miners are commonly flagged by heuristics. Verify the SHA-256 matches this page first, then choose "Run anyway" in Defender or add the file to its exclusions.',
            },
          },
          {
            q: { zh: 'Linux 报 GLIBC_2.38 not found / GLIBCXX not found？', en: 'Linux says GLIBC_2.38 not found / GLIBCXX not found?' },
            a: {
              zh: '那是 v1.21.0 及更早的版本在新基线上编的。换本页的 v1.21.1（老基线构建，glibc 2.17 以上通吃）即可，算法与算力完全一致。',
              en: 'That happens with v1.21.0 and earlier, which were built on a newer baseline. Switch to the v1.21.1 on this page (old-baseline build, glibc 2.17+) - algorithms and hashrate are identical.',
            },
          },
          {
            q: { zh: 'HiveOS 怎么用？', en: 'How do I use it on HiveOS?' },
            a: {
              zh: '当自定义锄头（Custom miner）填入即可：可执行文件用本页 Linux 版，参数照「一键上手」生成的 Linux 命令去掉文件名那部分。',
              en: 'Add it as a Custom miner: use the Linux build from this page, and for the arguments take the Linux command generated in Quick Setup without the executable name.',
            },
          },
          {
            q: { zh: '哪些算法能用显卡？', en: 'Which algorithms use the GPU?' },
            a: {
              zh: 'midstate（纯 GPU）、btx / qpow（GPU 或 CPU）。RandomX 系（rx/*、zoka）刻意抗 GPU，GPU 比 CPU 慢一个数量级，锄头只用 CPU 挖。挖 midstate 推荐用本站的 NTMminer-midstate 专用版（更小、换 job 损耗更低）。',
              en: 'midstate (GPU only) and btx / qpow (GPU or CPU). The RandomX family (rx/*, zoka) is deliberately GPU-hostile - a GPU is an order of magnitude slower than a CPU there, so the miner uses the CPU only. For midstate we recommend the dedicated NTMminer-midstate build on this site (smaller, lower job-switch loss).',
            },
          },
        ],
        changelog: [
          {
            version: 'v1.21.1', date: '2026-07-25',
            zh: 'Linux 版改用老基线（glibc 2.17）构建，解决 Ubuntu 22.04/20.04、HiveOS 上 GLIBC_2.38 / GLIBCXX_3.4.32 not found 无法启动；算法与算力和 v1.21.0 完全一致。Windows 版仅版本号变化。',
            en: 'Linux build moved to an old glibc 2.17 baseline, fixing the GLIBC_2.38 / GLIBCXX_3.4.32 not found startup failure on Ubuntu 22.04/20.04 and HiveOS; algorithms and hashrate identical to v1.21.0. Windows build: version string only.',
          },
          {
            version: 'v1.21.0', date: '2026-07-25',
            zh: '新增 Kadikama (rx/kad) 支持（CPU，RandomX v2 引擎）。',
            en: 'Added Kadikama (rx/kad) support (CPU, RandomX v2 engine).',
          },
          {
            version: 'v1.15.0', date: '2026-07-14',
            zh: 'Windows 版改为完全静态单文件，根治「找不到 libwinpthread-1.dll」。',
            en: 'Windows build is now a fully static single file, fixing the "libwinpthread-1.dll not found" error.',
          },
          {
            version: 'v1.14.0', date: '2026-07-12',
            zh: '原生多显卡：一进程驱动全部卡，新增 --gpu-devices。',
            en: 'Native multi-GPU: one process drives every card; new --gpu-devices flag.',
          },
        ],
        cli: {
          sourceNote: {
            zh: '事实源：2026-08-29 对与本页下载项逐字节一致（SHA-256 以 9aae7855 开头）的 v1.21.1 二进制实跑 --help。中文说明忠实转写原始输出；英文为对应翻译。',
            en: 'Source of truth: raw --help output run on 2026-08-29 from the v1.21.1 binary that is byte-for-byte identical to the download on this page (SHA-256 begins with 9aae7855). The Chinese descriptions transcribe that output faithfully; English is the translation.',
          },
          usage: [
            { label: '命名参数 / Named options', command: 'NTMminer -o <host:port> -u <wallet> [选项]' },
            { label: '旧位置式（兼容） / Legacy positional (compatible)', command: 'NTMminer <host> <port> <wallet> [bench_secs] [threads] [lanes]' },
          ],
          examples: [
            { label: '示例 1 · DragonX / Example 1 · DragonX', command: 'NTMminer-windows-x64-v1.21.1.exe -a rx/dragonx -o hk.ntmminer.com:3333 -u YOUR_WALLET_ADDRESS' },
            { label: '示例 2 · SOCKS5 / Example 2 · SOCKS5', command: 'NTMminer-windows-x64-v1.21.1.exe -a rx/dragonx -o hk.ntmminer.com:3333 -u YOUR_WALLET_ADDRESS --socks5 127.0.0.1:9050' },
          ],
          groups: [
            {
              id: 'connection', titleZh: '连接', titleEn: 'Connection',
              options: [
                { aliases: ['-o', '--url', '--pool-url'], value: '<host:port>', descriptionZh: '矿池地址（可带 stratum+tcp:// 前缀）', descriptionEn: 'Pool address (may include the stratum+tcp:// prefix).' },
                { aliases: ['-u', '--user', '--address'], value: '<wallet>', descriptionZh: '钱包/登录地址', descriptionEn: 'Wallet / login address.' },
                { aliases: ['--worker', '--rig-id'], value: '<名>', descriptionZh: 'worker 名（可选）', descriptionEn: 'Worker name (optional).' },
                { aliases: ['-p', '--pass'], value: '<pass>', defaultZh: 'x', defaultEn: 'x', descriptionZh: '登录密码（默认 x）', descriptionEn: 'Login password (default x).' },
                { aliases: ['--socks5', '-x'], value: '[user:pass@]host:port', descriptionZh: '经 SOCKS5 代理挖矿', descriptionEn: 'Mine through a SOCKS5 proxy.' },
              ],
            },
            {
              id: 'algorithm-hardware', titleZh: '算法与硬件', titleEn: 'Algorithms and hardware',
              options: [
                { aliases: ['-a', '--algo', '--coin'], value: '<名>', defaultZh: 'neuromorph', defaultEn: 'neuromorph', descriptionZh: '算法/币：neuromorph | argon2id-blocknet | midstate | rx/dragonx | zoka | rx/brva | rx/tar | rx/zeph | rx/dom | rx/scash | rx/kad | btx | qpow | btc09 | quark | velkarhash（默认 neuromorph）', descriptionEn: 'Algorithm / coin: neuromorph | argon2id-blocknet | midstate | rx/dragonx | zoka | rx/brva | rx/tar | rx/zeph | rx/dom | rx/scash | rx/kad | btx | qpow | btc09 | quark | velkarhash (default neuromorph).' },
                { aliases: ['-t', '--threads'], value: '<n>', defaultZh: '物理核数（关 SMT 配 MLP）', defaultEn: 'Physical core count (SMT off with MLP)', descriptionZh: '线程数（默认=物理核数，关 SMT 配 MLP；上限 256）', descriptionEn: 'Number of threads (default = physical core count, SMT off with MLP; maximum 256).' },
                { aliases: ['--smt'], value: null, defaultZh: '关', defaultEn: 'Off', descriptionZh: '改用全部逻辑核（SMT 全开）。大核机通常物理核+MLP 更快，故默认关', descriptionEn: 'Use all logical cores instead (SMT fully on). High-core-count machines are usually faster with physical cores + MLP, so this is off by default.' },
                { aliases: ['--gpu'], value: null, descriptionZh: '开 CUDA GPU 后端（midstate=纯 GPU；btx/qpow=CPU+GPU 双挖）。单二进制内置，需 NVIDIA 驱动', descriptionEn: 'Enable the CUDA GPU backend (midstate = GPU only; btx/qpow = CPU + GPU dual mining). Built into the single binary; needs an NVIDIA driver.', scopeZh: 'CUDA GPU 后端；help 明示 midstate、btx、qpow', scopeEn: 'CUDA GPU backend; help names midstate, btx and qpow' },
                { aliases: ['--gpu-only', '--no-cpu'], value: null, descriptionZh: '仅用 GPU 挖、不起 CPU worker（btx/qpow 关掉双挖的 CPU 边；隐含 --gpu）。GPU 矿机用这个', descriptionEn: 'Mine on the GPU only, no CPU workers (for btx/qpow this disables the CPU side of dual mining; implies --gpu). Use this on GPU rigs.', scopeZh: 'GPU 挖矿；btx/qpow 会关闭双挖的 CPU 边', scopeEn: 'GPU mining; disables the CPU side for btx/qpow' },
                { aliases: ['--gpu-devices'], value: '<列表>', defaultZh: '全部卡', defaultEn: 'All cards', descriptionZh: '多显卡：指定用哪几张卡（逗号列表如 0,1,3；缺省=全部卡）。隐含 --gpu', descriptionEn: 'Multiple GPUs: which cards to use (comma list such as 0,1,3; default = every card). Implies --gpu.', scopeZh: '多显卡；隐含 --gpu', scopeEn: 'Multi-GPU; implies --gpu' },
                { aliases: ['--stats-demo'], value: null, descriptionZh: '不连矿池，只刷新真实 NVML 状态（算力无 worker，显示 n/a）', descriptionEn: 'Do not connect to a pool; only refresh real NVML status (no hashrate worker, shown as n/a).', scopeZh: 'NVML 状态演示', scopeEn: 'NVML status demo' },
                { aliases: ['--huge-pages'], value: '<2m|1g|off>', defaultZh: '自动 2m', defaultEn: 'Automatic 2m', descriptionZh: '大页（默认自动 2m，包含 RandomX 系 dragonx/zoka/tar/zeph/dom）。对标 xmrig：Win 自动授予锁页特权、Linux(root) 自预留；1g 仅 dragonx dataset(实测 wash)', descriptionEn: 'Huge pages (default automatic 2m, including the RandomX family dragonx/zoka/tar/zeph/dom). As with xmrig: Windows grants the lock-pages privilege automatically, Linux (root) self-reserves; 1g only for the dragonx dataset (measured: no gain).', scopeZh: '2m：help 未注明算法限制；1g：仅 dragonx dataset', scopeEn: '2m: no algorithm limit stated; 1g: dragonx dataset only' },
                { aliases: ['--no-huge-pages'], value: null, descriptionZh: '= --huge-pages off（A/B 对照）', descriptionEn: '= --huge-pages off (for A/B comparison).', scopeZh: '与 --huge-pages 相同', scopeEn: 'Same as --huge-pages' },
                { aliases: ['--msr'], value: null, defaultZh: '关', defaultEn: 'Off', descriptionZh: 'RandomX 系启用 AMD Zen/Intel MSR 预取器调优（需 root+msr 模块；默认关，root-only 且机器相关；对拍 xmrigCC〔默认自写 MSR〕时用它对齐口径）', descriptionEn: 'RandomX family: enable AMD Zen / Intel MSR prefetcher tuning (needs root + the msr module; off by default, root-only and machine-dependent; use it to match xmrigCC, which writes MSRs by default).', scopeZh: '仅 RandomX 系', scopeEn: 'RandomX family only' },
                { aliases: ['--scash-epoch-duration'], value: '<秒>', defaultZh: '604800', defaultEn: '604800', descriptionZh: 'SCASH epoch 时长（默认 604800；regtest 用 86400）', descriptionEn: 'SCASH epoch duration (default 604800; 86400 on regtest).', scopeZh: '仅 rx/scash', scopeEn: 'rx/scash only' },
                { aliases: ['--lanes'], value: '<n|auto>', defaultZh: 'auto（启动满载自调）', defaultEn: 'auto (self-tuned under full load at start-up)', descriptionZh: 'MLP 交错 lane 数（NeuroMorph 专属）。默认 auto=启动满载自调；大核 EPYC 关键提速（单链喂不饱内存通道，K=2~4 倍增吞吐）。--lanes 1 关', descriptionEn: 'Number of interleaved MLP lanes (NeuroMorph only). Default auto = self-tune under full load at start-up; a key speedup on high-core-count EPYC (one chain cannot saturate the memory channels; K=2-4 multiplies throughput). --lanes 1 turns it off.', scopeZh: '仅 NeuroMorph', scopeEn: 'NeuroMorph only' },
              ],
            },
            {
              id: 'other', titleZh: '其它', titleEn: 'Other',
              options: [
                { aliases: ['--bench'], value: '<秒>', descriptionZh: '跑 N 秒自动退出（测试用）', descriptionEn: 'Run for N seconds then exit (for testing).' },
                { aliases: ['-h', '--help'], value: null, descriptionZh: '本帮助', descriptionEn: 'Show this help.' },
                { aliases: ['-V', '--version'], value: null, descriptionZh: '版本', descriptionEn: 'Show the version.' },
              ],
            },
          ],
        },
      },

      {
        id: 'midstate',
        name: 'NTMminer-midstate',
        kind: 'dedicated',
        version: 'v1.21.0',
        released: '2026-08-30',
        color: '#8451c9',
        logo: '/img/midstate.png',
        symbol: 'MDS',
        devFeePercent: 0,
        feeAddress: null,
        hardware: ['gpu'],
        tagline: {
          zh: 'midstate (MDS) 专用锄头：纯 GPU、1 MB、0% 抽水',
          en: 'The dedicated midstate (MDS) miner: GPU only, 1 MB, 0% dev fee',
        },
        summary: {
          zh: '只含 midstate 一个算法，比通用版小 30 倍，换 job 时的算力损耗更低。挖 MDS 请用这个，不要用通用版。',
          en: 'Contains the midstate algorithm only - 30x smaller than the universal build and lower loss when the pool switches jobs. Use this for MDS, not the universal build.',
        },
        algos: ['midstate'],
        requirements: {
          zh: 'NVIDIA 显卡，计算能力 ≥ 7.0（Volta V100 / RTX 20/30/40/50 系均可；GTX 10 系及更早的 Pascal 不支持）。只需 NVIDIA 驱动，不用装 CUDA。Windows 10/11 64 位，或任意 x86-64 Linux（glibc 2.17 以上，HiveOS 可当自定义锄头填入）。midstate 是纯 GPU 币，CPU 不参与。',
          en: 'An NVIDIA GPU with compute capability 7.0 or newer (Volta V100 and the RTX 20/30/40/50 series qualify; GTX 10-series and older Pascal are not supported). Only an NVIDIA driver is needed, no CUDA install. Windows 10/11 64-bit, or any x86-64 Linux (glibc 2.17+; on HiveOS add it as a custom miner). midstate is GPU-only; the CPU does not mine.',
        },
        files: [
          {
            id: 'midstate-win-x64',
            os: 'Windows x64',
            icon: 'windows',
            file: 'NTMminer-midstate-win-x64-v1.21.0.exe',
            run: 'NTMminer-midstate-win-x64-v1.21.0.exe',
            status: 'ready',
            size: 1038534,
            sha256: '6157f0a70dc5ef79fa8286640e9970e8b1c0e70edc2098b9ec370fc9cbc16bac',
            note: {
              zh: '单文件静态构建，无需任何 DLL，也无需安装 CUDA。首次运行若被 Defender 拦截，选「仍要运行」。',
              en: 'Single static executable, no DLLs and no CUDA install. If Defender warns on first run, choose "Run anyway".',
            },
          },
          {
            id: 'midstate-linux-x64',
            os: 'Linux x64 / HiveOS',
            icon: 'linux',
            file: 'NTMminer-midstate-linux-x64-v1.21.0',
            run: './NTMminer-midstate-linux-x64-v1.21.0',
            status: 'ready',
            size: 678768,
            sha256: 'd99fb7dccde9f25004a6001c1291e1f135103e27983cdb57573beafd414d883a',
            note: {
              zh: '老基线构建（glibc 2.17 以上即可，不会报 GLIBC 版本错），只需 NVIDIA 驱动。下载后 chmod +x 即可运行。',
              en: 'Old-baseline build (glibc 2.17+, no GLIBC version errors), needs only an NVIDIA driver. chmod +x and run.',
            },
          },
        ],
        quick: {
          workerDefault: 'rig1',
          intro: {
            zh: '选一个离你最近的接入点，填 MDS 地址，复制命令运行。--gpu-only 已经带上（midstate 是纯 GPU 币）。',
            en: 'Pick the endpoint nearest to you, fill in your MDS address, copy and run. --gpu-only is already included (midstate is GPU-only).',
          },
          targets: [],
        },
        highlights: [
          {
            title: { zh: '结算：coinbase 直付，矿池不打款', en: 'Settlement: coinbase direct-pay, no pool payouts' },
            body: {
              zh: '挖到块时，你的 95% 在爆块那一刻直接进你的地址（矿池抽 5% 池费）。看到 accepted 就是在正常提交份额。锄头本身 0% 抽水。',
              en: 'When a block is found your 95% lands in your address the moment it is mined (the pool keeps a 5% fee). Once you see accepted your shares are landing. The miner itself takes 0%.',
            },
          },
          {
            title: { zh: '地址是 64 位十六进制', en: 'The address is 64 hex characters' },
            body: {
              zh: '用官方钱包 midstate wallet create 生成。若你的钱包显示 72 位，只取前 64 位。',
              en: 'Created with the official midstate wallet create. If your wallet shows 72 hex characters, use only the first 64.',
            },
          },
          {
            title: { zh: 'v1.21.0 屏幕算力低 2~3% 是显示变准', en: 'v1.21.0 reads 2-3% lower on screen - that is accuracy, not slowdown' },
            body: {
              zh: '旧版把矿池换 job 时作废的那批也算进算力；新版只记已完成的工作，所以屏幕上的 nonce/s 比 v1.20.0 低 2%~3%，而池侧有效算力反而高 1.4%~7.7%。以矿池统计为准。要旧行为可加 --gpu-batch-secs 5。',
              en: 'The old build counted batches thrown away on a job switch; the new one counts only completed work, so on-screen nonce/s reads 2-3% below v1.20.0 while pool-side effective hashrate is 1.4-7.7% higher. Trust the pool statistics. Add --gpu-batch-secs 5 for the old behaviour.',
            },
          },
          {
            title: { zh: '多卡', en: 'Several cards' },
            body: {
              zh: '一进程驱动全部 NVIDIA 卡；--gpu-devices 0,1,2 指定用哪几张，不写默认全部。HiveOS 上当自定义锄头填入。',
              en: 'One process drives every NVIDIA card; --gpu-devices 0,1,2 picks specific cards, omit it to use all. On HiveOS add it as a custom miner.',
            },
          },
        ],
        faq: [
          {
            q: { zh: '为什么要用专用版，通用 NTMminer 不是也能挖 midstate？', en: 'Why the dedicated build - the universal NTMminer mines midstate too?' },
            a: {
              zh: '能挖，但专用版只有 1 MB，并且带 v1.21.0 的换 job 修复：矿池每分钟换一次 job 时白扔的算力从 4.7%（4090 达 8.7%）降到 1~2%，池侧有效算力提升 1.4%~7.7%（3060 +1.4 / 3080 Ti +1.9 / 4070 +2.6 / 5090 D +3.8 / 4090 +7.7）。通用版目前还是旧的 5 秒批次。',
              en: 'It can, but the dedicated build is 1 MB and carries the v1.21.0 job-switch fix: with the pool switching jobs about once a minute, wasted work drops from 4.7% (up to 8.7% on a 4090) to 1-2%, lifting pool-side effective hashrate by 1.4-7.7% (3060 +1.4, 3080 Ti +1.9, 4070 +2.6, 5090 D +3.8, 4090 +7.7). The universal build still uses the old 5-second batches.',
            },
          },
          {
            q: { zh: '我的显卡能用吗？', en: 'Will my card work?' },
            a: {
              zh: '需要计算能力 ≥ 7.0：V100、RTX 20/30/40/50 系都可以；GTX 10 系及更早的 Pascal 不支持。只需 NVIDIA 驱动。',
              en: 'Compute capability 7.0 or newer is required: V100 and the RTX 20/30/40/50 series qualify; GTX 10-series and older Pascal are not supported. Only an NVIDIA driver is needed.',
            },
          },
          {
            q: { zh: '支付页为什么是空的？', en: 'Why is the payments page empty?' },
            a: {
              zh: 'midstate 是 coinbase 直付：奖励在爆块那一刻直接写进你的地址，矿池不另外打款，所以看区块页而不是支付页。',
              en: 'midstate pays through the coinbase: the reward is written straight into your address when the block is found, the pool sends no separate payout - look at the blocks page, not payments.',
            },
          },
        ],
        changelog: [
          {
            version: 'v1.21.0', date: '2026-08-30',
            zh: '修复「矿池换 job 时在飞批次整批作废」：每批工作量从 ~5 秒缩到 1~2 秒（按显卡自适应 1~2 整波），白扔的算力从 4.7% 降到 1~2%，池侧有效算力 +1.4%~7.7%。算力显示改为只计已完成的工作。新增 --gpu-batch-secs。内核与共识逻辑未动。',
            en: 'Fixed the "in-flight batch thrown away on job switch" loss: each batch now holds 1-2 s of work (1-2 full waves chosen per GPU) instead of ~5 s, wasted work drops from 4.7% to 1-2%, pool-side effective hashrate +1.4-7.7%. On-screen hashrate now counts completed work only. New --gpu-batch-secs flag. Kernel and consensus code unchanged.',
          },
          {
            version: 'v1.20.0', date: '2026-08-28',
            zh: '首个 midstate 专用版：只含 midstate 算法，Windows 1.04 MB / Linux 0.68 MB。',
            en: 'First dedicated midstate build: midstate algorithm only, Windows 1.04 MB / Linux 0.68 MB.',
          },
        ],
        cli: {
          sourceNote: {
            zh: '事实源：2026-08-29 对与本页下载项逐字节一致（SHA-256 以 6157f0a7 开头）的 v1.21.0 二进制实跑 --help。本二进制只含 midstate 一个算法：-a 只接受 midstate，传其它算法会直接退出；help 里同时列出的 CPU 参数（-t / --smt / --huge-pages / --msr / --lanes 等）对纯 GPU 的 midstate 不起作用，下表只列有效参数。',
            en: 'Source of truth: raw --help output run on 2026-08-29 from the v1.21.0 binary byte-for-byte identical to the download on this page (SHA-256 begins with 6157f0a7). This binary contains the midstate algorithm only: -a accepts midstate alone and any other algorithm exits immediately; the CPU flags the help also lists (-t / --smt / --huge-pages / --msr / --lanes ...) have no effect on GPU-only midstate, so the table lists only the flags that matter.',
          },
          usage: [
            { label: 'Windows', command: 'NTMminer-midstate-win-x64-v1.21.0.exe -a midstate -o <host:port> -u <MDS地址> --gpu-only [--worker <名>] [--gpu-devices 0,1]' },
            { label: 'Linux / HiveOS', command: './NTMminer-midstate-linux-x64-v1.21.0 -a midstate -o <host:port> -u <MDS地址> --gpu-only [--worker <名>] [--gpu-devices 0,1]' },
          ],
          examples: [
            { label: '示例 · 全部显卡 / Example · all cards', command: 'NTMminer-midstate-win-x64-v1.21.0.exe -a midstate -o hk.ntmminer.com:13333 -u YOUR_MDS_ADDRESS --gpu-only --worker rig1' },
            { label: '示例 · 只用 0,2 两张卡 / Example · cards 0 and 2 only', command: './NTMminer-midstate-linux-x64-v1.21.0 -a midstate -o hk.ntmminer.com:13333 -u YOUR_MDS_ADDRESS --gpu-only --gpu-devices 0,2 --worker rig1' },
          ],
          groups: [
            {
              id: 'connection', titleZh: '连接', titleEn: 'Connection',
              options: [
                { aliases: ['-o', '--url', '--pool-url'], value: '<host:port>', descriptionZh: '矿池地址（可带 stratum+tcp:// 前缀）。本站 midstate 池：hk / hk3 / us / eur / sgp / ca.ntmminer.com:13333', descriptionEn: 'Pool address (may include the stratum+tcp:// prefix). This site\'s midstate pool: hk / hk3 / us / eur / sgp / ca.ntmminer.com:13333' },
                { aliases: ['-u', '--user', '--address'], value: '<wallet>', descriptionZh: '你的 MDS 地址（64 位十六进制；钱包显示 72 位则取前 64 位）', descriptionEn: 'Your MDS address (64 hex; if the wallet shows 72, use the first 64).' },
                { aliases: ['--worker', '--rig-id'], value: '<名>', descriptionZh: 'worker 名（可选，多台机器时便于区分）', descriptionEn: 'Worker name (optional; tells your rigs apart).' },
                { aliases: ['-p', '--pass'], value: '<pass>', defaultZh: 'x', defaultEn: 'x', descriptionZh: '登录密码（默认 x）', descriptionEn: 'Login password (default x).' },
                { aliases: ['--socks5', '-x'], value: '[user:pass@]host:port', descriptionZh: '经 SOCKS5 代理挖矿', descriptionEn: 'Mine through a SOCKS5 proxy.' },
              ],
            },
            {
              id: 'gpu', titleZh: '算法与 GPU', titleEn: 'Algorithm and GPU',
              options: [
                { aliases: ['-a', '--algo', '--coin'], value: 'midstate', defaultZh: 'neuromorph（本二进制不含，必须显式写 -a midstate）', defaultEn: 'neuromorph (not in this binary - always pass -a midstate)', descriptionZh: '算法。本二进制只接受 midstate', descriptionEn: 'Algorithm. This binary accepts midstate only.', scopeZh: '仅 midstate', scopeEn: 'midstate only' },
                { aliases: ['--gpu-only', '--no-cpu'], value: null, descriptionZh: '仅用 GPU 挖、不起 CPU worker（隐含 --gpu）。midstate 必须加', descriptionEn: 'Mine on the GPU only, no CPU workers (implies --gpu). Mandatory for midstate.', scopeZh: 'midstate 必需', scopeEn: 'Required for midstate' },
                { aliases: ['--gpu'], value: null, descriptionZh: '开 CUDA GPU 后端（midstate=纯 GPU）。单二进制内置，需 NVIDIA 驱动', descriptionEn: 'Enable the CUDA GPU backend (midstate = GPU only). Built in; needs an NVIDIA driver.', scopeZh: '被 --gpu-only 隐含', scopeEn: 'Implied by --gpu-only' },
                { aliases: ['--gpu-devices'], value: '<列表>', defaultZh: '全部卡', defaultEn: 'All cards', descriptionZh: '多显卡：指定用哪几张卡（逗号列表如 0,1,3；缺省=全部卡）。隐含 --gpu', descriptionEn: 'Multiple GPUs: which cards to use (comma list such as 0,1,3; default = every card). Implies --gpu.', scopeZh: '多显卡', scopeEn: 'Multi-GPU' },
                { aliases: ['--gpu-batch-secs'], value: '<秒>', defaultZh: '1.0', defaultEn: '1.0', descriptionZh: 'midstate GPU 每批工作量上限（默认 1.0；批越大换 job 时白扔越多，至少 2 整波）。取值 0.05~60', descriptionEn: 'Upper bound on the work per GPU batch (default 1.0; larger batches waste more on a job switch; never below 2 full waves). Range 0.05-60.', scopeZh: 'v1.21.0 新增', scopeEn: 'New in v1.21.0' },
                { aliases: ['--stats-demo'], value: null, descriptionZh: '不连矿池，只刷新真实 NVML 状态（算力无 worker，显示 n/a）', descriptionEn: 'Do not connect to a pool; only refresh real NVML status (no hashrate worker, shown as n/a).', scopeZh: 'NVML 状态演示', scopeEn: 'NVML status demo' },
              ],
            },
            {
              id: 'other', titleZh: '其它', titleEn: 'Other',
              options: [
                { aliases: ['--bench'], value: '<秒>', descriptionZh: '跑 N 秒自动退出（测试用）', descriptionEn: 'Run for N seconds then exit (for testing).' },
                { aliases: ['-h', '--help'], value: null, descriptionZh: '本帮助', descriptionEn: 'Show this help.' },
                { aliases: ['-V', '--version'], value: null, descriptionZh: '版本', descriptionEn: 'Show the version.' },
              ],
            },
          ],
        },
      },

      {
        id: 'noid',
        name: 'NTMminer-noid',
        kind: 'dedicated',
        version: 'v1.1.2',
        released: '2026-08-30',
        color: '#0f766e',
        logo: null,
        symbol: 'NOID',
        devFeePercent: 3,
        feeAddress: 'o1z8q6nz9dyzucd7w8evv95aufpsunrkwjtmcy6hdf88hxfl28rpys86zlcz',
        hardware: ['cpu', 'gpu'],
        tagline: {
          zh: 'ParanO(1)d (NOID) 专用锄头：CPU + NVIDIA GPU，抽水 3%',
          en: 'The dedicated ParanO(1)d (NOID) miner: CPU + NVIDIA GPU, 3% dev fee',
        },
        summary: {
          zh: '接第三方 NOID 矿池（本站不运营 NOID 池）。驱动 ≥ 580 的 RTX 30/40/50 走全速内核，同卡对拍比 hashborn 快 4%~7%。',
          en: 'Connects to third-party NOID pools (this site runs no NOID pool). RTX 30/40/50 on driver 580+ get the full-speed kernel, 4-7% faster than hashborn on the same card.',
        },
        algos: ['ParanO(1)d (NOID) · Poseidon2b'],
        requirements: {
          zh: 'CPU：任意 x86-64。GPU：NVIDIA 显卡计算能力 ≥ 7.0（V100 / GTX 16 / RTX 20/30/40/50 / A 系列）；驱动 ≥ 580（CUDA 13）的 RTX 30/40/50 走 clmad 内核 = 全速，驱动 < 580 或 Volta/Turing 自动走可移植内核（约全速的 40%，启动横幅会写明走的是哪条）。只需 NVIDIA 驱动，不用装 CUDA。Windows 10/11 64 位，或任意 x86-64 Linux（glibc 2.17 以上，HiveOS 可当自定义锄头）。',
          en: 'CPU: any x86-64. GPU: NVIDIA with compute capability 7.0 or newer (V100, GTX 16, RTX 20/30/40/50, A-series); RTX 30/40/50 on driver 580+ (CUDA 13) run the clmad kernel at full speed, driver below 580, Volta and Turing fall back to the portable kernel automatically (about 40% of full speed; the start-up banner says which one you got). Only an NVIDIA driver is needed, no CUDA install. Windows 10/11 64-bit, or any x86-64 Linux (glibc 2.17+; HiveOS as a custom miner).',
        },
        files: [
          {
            id: 'noid-windows-x64',
            os: 'Windows x64',
            icon: 'windows',
            file: 'NTMminer-noid-v1.1.2-windows-x64.zip',
            run: 'ntmminer-noid.exe',
            status: 'ready',
            size: 11690207,
            sha256: '5e91b8b3c27bef25d421e9067f96a7e0eca70abbb057ec3a12e584f05cea2271',
            innerSha256: { file: 'ntmminer-noid.exe', sha256: '3b77d009f76e83a024e864ff1e2d8615f9ef1e3c0e3110a59066b30494d9f101' },
            note: {
              zh: 'zip 内含 ntmminer-noid.exe、接两个池的「挖矿-*.bat」（改一行地址、双击即挖、退出自动重启）、自检.bat 与完整《使用说明》。单文件静态构建，无需 DLL，无需安装 CUDA。首次运行若被 Defender 拦截，选「仍要运行」。',
              en: 'The zip holds ntmminer-noid.exe, one pool .bat per pool (edit one line for the address, double-click to mine, restarts on exit), a self-test .bat and the full manual (Chinese). Single static executable, no DLLs, no CUDA install. If Defender warns on first run, choose "Run anyway".',
            },
          },
          {
            id: 'noid-linux-x64',
            os: 'Linux x64 / HiveOS',
            icon: 'linux',
            file: 'NTMminer-noid-v1.1.2-linux-x64.tar.gz',
            run: './ntmminer-noid',
            status: 'ready',
            size: 11854180,
            sha256: '822df21c1cdd76c49266ff94d2495b7f6e032adb8944fcbddab092cd130c0fa6',
            innerSha256: { file: 'ntmminer-noid', sha256: '00491da5e4b8a253fc9fbbe7ce7c2638ef17a8bd60fbfa843443a94789d54587' },
            note: {
              zh: 'tar.gz 内含 ntmminer-noid、接池脚本 mine-*.sh、systemd 常驻示例 ntmminer-noid.service.example 与完整《使用说明》。老基线构建（glibc 2.17 以上），只需 NVIDIA 驱动。tar xzf 解压后 chmod +x ntmminer-noid。',
              en: 'The tar.gz holds ntmminer-noid, the pool scripts mine-*.sh, a systemd unit example ntmminer-noid.service.example and the full manual (Chinese). Old-baseline build (glibc 2.17+), needs only an NVIDIA driver. tar xzf, then chmod +x ntmminer-noid.',
            },
          },
        ],
        quick: {
          workerDefault: 'rig1',
          intro: {
            zh: '选一个矿池，填你的 NOID 收款地址（o1 开头），复制命令运行。两个池认人的方式不一样（一个看 --coinbase，一个看 --key），生成器已经替你写对了。',
            en: 'Pick a pool, fill in your NOID payout address (starts with o1), copy and run. The two pools identify you differently (one by --coinbase, one by --key); the generator writes the right form for you.',
          },
          address: {
            label: { zh: 'NOID 收款地址', en: 'NOID payout address' },
            placeholder: { zh: 'o1 开头的 bech32m 地址', en: 'bech32m address starting with o1' },
            commandPlaceholder: 'YOUR_NOID_ADDRESS',
            pattern: '^o1[qpzry9x8gf2tvdw0s3jn54khce6mua7l]{20,}$',
            hint: { zh: 'o1 开头、小写字母和数字。地址不对 = 挖到的全部记在别人名下，请仔细核对。', en: 'Starts with o1, lowercase letters and digits. A wrong address books every share to somebody else - check it carefully.' },
          },
          modes: [
            { id: 'gpu', label: { zh: '显卡（--gpu）', en: 'GPU (--gpu)' }, args: '--gpu' },
            { id: 'cpu', label: { zh: 'CPU（全部核心）', en: 'CPU (all cores)' }, args: '' },
          ],
          targets: [
            {
              id: 'parano1d-pool',
              thirdParty: true,
              name: 'parano1d-pool.fun',
              symbol: 'NOID',
              label: { zh: 'parano1d-pool.fun（池费 3%，按轮比例分配）', en: 'parano1d-pool.fun (3% fee, proportional per round)' },
              site: 'https://parano1d-pool.fun/',
              endpoints: [{ region: 'EU', url: 'http://parano1d-pool.fun:3784', label: { zh: '明文 HTTP', en: 'plain HTTP' } }],
              template: '{bin} --rpc {url} --coinbase {address} --worker {worker} {mode}',
              note: {
                zh: '这个池按 submitBlock 里带的地址记账：--coinbase 就是收款地址，--worker 是矿机名。',
                en: 'This pool books shares to the address carried in submitBlock: --coinbase is the payout address, --worker the rig name.',
              },
            },
            {
              id: 'ariabrain',
              thirdParty: true,
              name: 'pool.ariabrain.com',
              symbol: 'NOID',
              label: { zh: 'pool.ariabrain.com（池费 1%，PPLNS，2 小时打款，起付 10 NOID）', en: 'pool.ariabrain.com (1% fee, PPLNS, payouts every 2 h, minimum 10 NOID)' },
              site: 'https://pool.ariabrain.com/noid.html',
              endpoints: [{ region: 'HTTPS', url: 'https://pool.ariabrain.com/noid-rpc/', label: { zh: '结尾斜杠必须带', en: 'trailing slash required' } }],
              template: '{bin} --rpc {url} --key {address}.{worker} {mode}',
              note: {
                zh: '这个池用 --key 认人，而 --key 就是「你的地址.矿机名」，不用再写 --coinbase / --worker。',
                en: 'This pool identifies you by --key, which is "your address.rig name"; --coinbase / --worker are not needed.',
              },
            },
          ],
        },
        highlights: [
          {
            title: { zh: '抽水 3%，明码公示、可自行核对', en: '3% dev fee, disclosed and checkable' },
            body: {
              zh: '收款地址 o1z8q6nz9dyzucd7w8evv95aufpsunrkwjtmcy6hdf88hxfl28rpys86zlcz，与启动横幅、程序内参数一致。每约 33 张模板有 1 张用抽水地址取；按本机累计挖矿时长记账（台账存本地），反复重启不会重抽，任何时刻实际比例都不超过 3%。日志每行 hashrate 后都跟着实际累计抽水率。',
              en: 'Fee address o1z8q6nz9dyzucd7w8evv95aufpsunrkwjtmcy6hdf88hxfl28rpys86zlcz, identical to the start-up banner and the built-in parameter. About 1 template in 33 is fetched for the fee address; the fee is accounted against cumulative mining time (ledger kept locally), so restarts never re-charge it and the realised share never exceeds 3%. Every hashrate log line prints the realised fee share.',
            },
          },
          {
            title: { zh: '实测算力（同卡交替对拍，3 轮取值）', en: 'Measured hashrate (alternating on the same card, 3 rounds)' },
            body: {
              zh: '驱动 ≥ 580（clmad 内核）：RTX 5090 192.3 MH/s（hashborn 0.2.5：179.1，+7.3%）、RTX 4090 D 128.7（122.6，+5%）、RTX 4080 87.2（83.7，+4%）、RTX 3080 Ti 52.3~53.7（49.9~50.3，+5~6%）、RTX 5060 Ti 41.8（39.4，+6.2%）。驱动 550（可移植内核，hashborn 在这些机器上无法启动）：RTX 4090 60.9、RTX 4070 23.1、RTX 3060 9.87、Tesla V100 12.7~13.0，比 v1.1.0 高 14%~26%。抽水各按自己公示扣：本程序 3%，hashborn 5%。',
              en: 'Driver 580+ (clmad kernel): RTX 5090 192.3 MH/s (hashborn 0.2.5: 179.1, +7.3%), RTX 4090 D 128.7 (122.6, +5%), RTX 4080 87.2 (83.7, +4%), RTX 3080 Ti 52.3-53.7 (49.9-50.3, +5-6%), RTX 5060 Ti 41.8 (39.4, +6.2%). Driver 550 (portable kernel; hashborn cannot start on these machines): RTX 4090 60.9, RTX 4070 23.1, RTX 3060 9.87, Tesla V100 12.7-13.0, 14-26% above v1.1.0. Dev fees are deducted per program: 3% here, 5% for hashborn.',
            },
          },
          {
            title: { zh: '第三方矿池', en: 'Third-party pools' },
            body: {
              zh: '本站不运营 NOID 矿池，本页只提供锄头。池费与打款规则以各池自己的公示为准（说明成文时：parano1d-pool.fun 3%、pool.ariabrain.com 1% PPLNS）。',
              en: 'This site runs no NOID pool; this page only offers the miner. Fees and payout rules are whatever each pool publishes (at the time of writing: parano1d-pool.fun 3%, pool.ariabrain.com 1% PPLNS).',
            },
          },
          {
            title: { zh: '先自检，省 90% 的麻烦', en: 'Self-test first - it saves most of the trouble' },
            body: {
              zh: 'Windows 双击 zip 里的「自检.bat」，或手动跑 --check-gpu、--selftest、--gpu-selftest（共识逐字节对官方 oracle）和 --gpu-bench 6（离线跑分 6 秒）。每一项都要 PASS 再接池。',
              en: 'On Windows double-click the self-test .bat in the zip, or run --check-gpu, --selftest, --gpu-selftest (consensus byte-for-byte against the official oracle) and --gpu-bench 6 (6-second offline benchmark) by hand. Every item must PASS before you connect to a pool.',
            },
          },
        ],
        faq: [
          {
            q: { zh: '启动横幅说走的是「可移植内核」，怎么提速？', en: 'The banner says "portable kernel" - how do I get full speed?' },
            a: {
              zh: 'RTX 30/40/50 系把 NVIDIA 驱动升到 ≥ 580 就会自动切到 clmad 内核，算力约再 ×1.5~2。Volta / Turing（V100、GTX 16、RTX 20）没有 clmad 路径，可移植内核就是它们的全速。',
              en: 'On RTX 30/40/50 cards upgrade the NVIDIA driver to 580 or newer and the miner switches to the clmad kernel automatically, roughly another x1.5-2. Volta / Turing (V100, GTX 16, RTX 20) have no clmad path; the portable kernel is their full speed.',
            },
          },
          {
            q: { zh: '算力正常但一直没收益？', en: 'Hashrate looks fine but nothing is credited?' },
            a: {
              zh: '几乎都是地址填错。parano1d-pool.fun 只认 --coinbase 里的地址，pool.ariabrain.com 只认 --key 的「地址.矿机名」；地址不对，每一份 share 都记在别人名下。到矿池网页用你的地址查一下有没有算力。',
              en: 'Almost always a wrong address. parano1d-pool.fun only looks at --coinbase; pool.ariabrain.com only at the "address.rig" in --key. With a wrong address every share is booked to somebody else. Look your address up on the pool\'s web page to confirm it shows hashrate.',
            },
          },
          {
            q: { zh: 'Linux 想后台常驻怎么办？', en: 'How do I keep it running in the background on Linux?' },
            a: {
              zh: 'tar.gz 里有 ntmminer-noid.service.example：拷到 /etc/systemd/system/ntmminer-noid.service，改一行地址，systemctl enable --now ntmminer-noid。注意抽水台账写在运行用户的 HOME 下（~/.ntmminer-noid-devfee），User= 指定的用户要有可写的 HOME。',
              en: 'The tar.gz ships ntmminer-noid.service.example: copy it to /etc/systemd/system/ntmminer-noid.service, edit the address line, systemctl enable --now ntmminer-noid. The fee ledger lives in the running user\'s HOME (~/.ntmminer-noid-devfee), so the User= you pick needs a writable HOME.',
            },
          },
          {
            q: { zh: '能 solo 挖自己的节点吗？', en: 'Can I solo-mine against my own node?' },
            a: {
              zh: '可以：--rpc http://127.0.0.1:9601 [--gpu]。节点自己出模板，锄头只做 PoW；收款地址默认是节点配置的地址，除非节点带 --allow-custom-coinbase 启动。',
              en: 'Yes: --rpc http://127.0.0.1:9601 [--gpu]. The node builds the template and the miner only does PoW; the payout address is the node\'s own unless the node was started with --allow-custom-coinbase.',
            },
          },
        ],
        changelog: [
          {
            version: 'v1.1.2', date: '2026-08-30',
            zh: '修复接 AriaPool（pool.ariabrain.com 这类按 Bearer key 认人的池）时抽水收不到的问题：这类池按「谁取的模板」记账，抽水槽改用抽水地址去取模板后，已在真池核实抽水正确入账。算力与内核和 v1.1.1 逐位相同，你自己的挖矿收益不受影响。',
            en: 'Fixed dev-fee collection on AriaPool-style bearer-key pools (pool.ariabrain.com): they credit whoever fetched the template, so the fee slot now fetches under the dev address — verified crediting correctly on the live pool. Hashrate and kernels are bit-identical to v1.1.1; your own earnings are unaffected.',
          },
          {
            version: 'v1.1.1', date: '2026-08-29',
            zh: '可移植内核（驱动 < 580 / Volta / Turing）提速 14%~26%（4090 60.9、4070 23.1、3060 9.87、V100 12.7~13.0 MH/s）；修复 RTX 50 系装 570~579 驱动时 fatbin 加载失败（补 sm_100/120）。驱动 ≥ 580 的 clmad 内核与 v1.1.0 逐位相同。',
            en: 'Portable kernel (driver < 580 / Volta / Turing) 14-26% faster (4090 60.9, 4070 23.1, 3060 9.87, V100 12.7-13.0 MH/s); fixed the fatbin load failure on RTX 50 cards with 570-579 drivers (added sm_100/120). The clmad kernel used on driver 580+ is bit-identical to v1.1.0.',
          },
          {
            version: 'v1.1.0', date: '2026-08-29',
            zh: 'GPU 内核换 GF((2^64)^2) 表示（--gpu-rep r），驱动 ≥ 580 的 RTX 30/40/50 同卡对拍反超 hashborn 0.2.5 4%~7%；新增 --gpu-lanes / --gpu-tab。',
            en: 'GPU kernel moved to the GF((2^64)^2) representation (--gpu-rep r); RTX 30/40/50 on driver 580+ beat hashborn 0.2.5 by 4-7% on the same card; new --gpu-lanes / --gpu-tab flags.',
          },
        ],
        cli: {
          sourceNote: {
            zh: '事实源：2026-08-29 对 zip/tar.gz 内的 v1.1.2 二进制（ntmminer-noid.exe SHA-256 以 3b77d009 开头）实跑 --help。原始输出为英文；中文为对应翻译，英文忠实转写。',
            en: 'Source of truth: raw --help output run on 2026-08-29 from the v1.1.2 binary inside the zip / tar.gz (ntmminer-noid.exe SHA-256 begins with 3b77d009). The original output is English and is transcribed faithfully; Chinese is the translation.',
          },
          usage: [
            { label: 'POOL · parano1d-pool.fun', command: 'ntmminer-noid.exe --rpc http://parano1d-pool.fun:3784 --coinbase <o1address> --worker <name> [--gpu]' },
            { label: 'POOL · pool.ariabrain.com', command: 'ntmminer-noid.exe --rpc https://pool.ariabrain.com/noid-rpc/ --key <o1address>.<name> [--gpu]' },
            { label: 'SOLO · 自己的节点 / your own node', command: 'ntmminer-noid.exe --rpc http://127.0.0.1:9601 [--coinbase <o1address>] [--gpu]' },
          ],
          examples: [
            { label: '自检 / Self-test', command: 'ntmminer-noid.exe --check-gpu && ntmminer-noid.exe --gpu-selftest' },
            { label: '离线跑分 6 秒 / Offline benchmark, 6 s', command: 'ntmminer-noid.exe --gpu-bench 6' },
          ],
          groups: [
            {
              id: 'pool', titleZh: '连接与矿池', titleEn: 'Connection and pool',
              options: [
                { aliases: ['--rpc'], value: '<URL>', defaultZh: 'http://127.0.0.1:9601', defaultEn: 'http://127.0.0.1:9601', descriptionZh: 'ParanO(1)d 节点或矿池的 JSON-RPC 端点', descriptionEn: 'JSON-RPC endpoint of the ParanO(1)d node or pool.' },
                { aliases: ['--key'], value: '<TOKEN>', descriptionZh: '矿池 / 外部 RPC 的 Bearer token。须与节点的 --mining-key 一致；用默认 127.0.0.1 绑定 solo 挖矿时不需要。pool.ariabrain.com 用它认人：填「地址.矿机名」', descriptionEn: 'Bearer token for pool / external RPC access. Must match the node\'s --mining-key; not needed for solo mining on the default 127.0.0.1 binding. pool.ariabrain.com identifies you by it: "address.rig".', scopeZh: 'pool-key 类矿池 / 外部 RPC', scopeEn: 'pool-key pools / external RPC' },
                { aliases: ['--coinbase'], value: '<ADDRESS>', defaultZh: '空（用节点配置的地址）', defaultEn: 'empty (the node\'s configured address)', descriptionZh: '你自己的收款地址（bech32m o1…）。solo 时只有节点带 --allow-custom-coinbase 启动才生效；留空则用节点配置的地址（池模式）。parano1d-pool.fun 用它认人', descriptionEn: 'Your own payout address (bech32m o1...). Solo: only works when the node was started with --allow-custom-coinbase; leave empty to use the node\'s configured address (pool mode). parano1d-pool.fun identifies you by it.' },
                { aliases: ['--worker'], value: '<NAME>', defaultZh: '本机主机名', defaultEn: 'this machine\'s hostname', descriptionZh: '这台机器向矿池报的名字。solo 时忽略', descriptionEn: 'Name this machine reports to the pool. Ignored when mining solo.' },
                { aliases: ['--peer'], value: '<auto|node|pool-submit|pool-key>', defaultZh: 'auto', defaultEn: 'auto', descriptionZh: '强制指定对端类型。auto=启动时询问对端；node=parano1d 全节点（solo）；pool-submit=从 submitBlock 里读收款人的池（parano1d-pool.fun）；pool-key=从 Bearer key 里读收款人的池（AriaPool）。其余是给「回答方式和实测的两个池不同」的池留的逃生口', descriptionEn: 'Override what the far end is taken to be. auto = ask the endpoint at start-up; node = a parano1d full node (solo); pool-submit = a pool that reads the payee out of submitBlock (parano1d-pool.fun); pool-key = a pool that reads the payee out of the bearer key (AriaPool). The rest are escape hatches for a pool that answers differently from the two we measured.' },
                { aliases: ['--pool-inflight'], value: '<N>', defaultZh: '64', defaultEn: '64', descriptionZh: '同时在飞往矿池的 share 数上限。share 由独立线程发送、从不打断搜索，这里只限制有多少个往返可以重叠；矿池离得远就调大', descriptionEn: 'How many shares may be in flight towards the pool at once. Shares are shipped by their own threads and never stop the search; this only caps how many round trips overlap. Raise it if the pool is far away.' },
                { aliases: ['--pool-poll-ms'], value: '<MS>', defaultZh: '250', defaultEn: '250', descriptionZh: '多久问一次矿池 job 有没有变（毫秒）。池模式下只有它能结束一次搜索，所以它也是「挖一个死 job」的时长上限', descriptionEn: 'How often to ask the pool whether the job changed, in milliseconds. This is the only thing that ends a search in pool mode, so it is also the upper bound on how long we can mine a dead job.' },
                { aliases: ['--poll-ms'], value: '<MS>', defaultZh: '500', defaultEn: '500', descriptionZh: '节点持续下发已作废模板时，重取前等待的毫秒数（出解和 stale 会立即重取）', descriptionEn: 'Milliseconds to wait before re-fetching when the node keeps serving an already-cancelled template (solves and stales refetch instantly).' },
                { aliases: ['--log'], value: '<LEVEL>', defaultZh: 'info', defaultEn: 'info', descriptionZh: '日志级别（error | warn | info | debug）。为兼容官方锄头的命令行而接受', descriptionEn: 'Log level (error | warn | info | debug). Accepted for official-miner CLI compatibility.' },
              ],
            },
            {
              id: 'cpu', titleZh: 'CPU 与自检', titleEn: 'CPU and self-tests',
              options: [
                { aliases: ['--threads'], value: '<N>', defaultZh: '0（全部逻辑核）', defaultEn: '0 (every logical CPU)', descriptionZh: 'PoW 线程数。0 = 进程能看到的全部逻辑核', descriptionEn: 'Number of PoW threads. 0 = every logical CPU visible to the process.' },
                { aliases: ['--midstate'], value: '<on|off>', defaultZh: 'on', defaultEn: 'on', descriptionZh: '哈希引擎。on = midstate 快路径（默认），off = 官方全哈希路径（用于 A/B 验证提速）', descriptionEn: 'Hashing engine. on = midstate fast path (default), off = official full-hash path (for A/B verification of the speedup).' },
                { aliases: ['--check-hardware'], value: null, descriptionZh: '检查本机 CPU 是否满足生产要求，不连节点，然后退出', descriptionEn: 'Check production CPU support and exit without connecting to a node.' },
                { aliases: ['--selftest'], value: null, descriptionZh: '跑完整共识自检（≥ 200 组随机模板加边界用例，全部对 noid_chain oracle 核对）然后退出', descriptionEn: 'Run the full consensus self-test (200+ random template groups plus edge cases, all verified against the noid_chain oracle) and exit.' },
                { aliases: ['--bench'], value: '<N_MILLION>', descriptionZh: '离线跑分：全部线程哈希 N 百万个 nonce，报告 H/s', descriptionEn: 'Offline benchmark: hash N million nonces on all threads, report H/s.' },
              ],
            },
            {
              id: 'gpu', titleZh: 'GPU', titleEn: 'GPU',
              options: [
                { aliases: ['--gpu'], value: null, descriptionZh: '用 GPU 挖而不是 CPU（NVIDIA，计算能力 ≥ 7.0；≥ 8.0 且 CUDA 13 驱动才走快速 clmad 内核）', descriptionEn: 'Mine on the GPU instead of the CPU (NVIDIA, compute capability >= 7.0; >= 8.0 with a CUDA 13 driver for the fast clmad kernels).' },
                { aliases: ['--gpu-devices'], value: '<LIST>', defaultZh: '全部支持的卡', defaultEn: 'every supported device', descriptionZh: '用哪几张卡，如 0,2', descriptionEn: 'Which GPUs to use, e.g. 0,2.' },
                { aliases: ['--gpu-blocks'], value: '<N>', defaultZh: '0（自动）', defaultEn: '0 (auto)', descriptionZh: '每次启动的 block 数。0 = 按显卡 SM 数自动（已调优的默认值），一般不用动', descriptionEn: 'Blocks per launch. 0 = tuned default derived from the device\'s SM count; normally leave it.' },
                { aliases: ['--gpu-threads'], value: '<N>', defaultZh: '0（自动）', defaultEn: '0 (auto)', descriptionZh: '每 block 线程数。0 = 自动：R 内核按寄存器预算取最宽的 block（每 SM 一个 block；RTX 4080 为 1024），flat 内核 256', descriptionEn: 'Threads per block. 0 = automatic: the R kernels take the widest block the register budget allows (one block per SM; RTX 4080: 1024), flat kernels 256.' },
                { aliases: ['--gpu-rep'], value: '<auto|r|flat>', defaultZh: 'auto', defaultEn: 'auto', descriptionZh: '置换运行在哪种域表示：auto / r = GF((2^64)^2)（RTX 4080 83 MH/s，flat 为 55），flat = 2026-08-23 的旧内核（仅作回退）', descriptionEn: 'Field representation the permutation runs in: auto / r = GF((2^64)^2) (RTX 4080 83 MH/s vs 55 flat), flat = the 2026-08-23 kernels (fallback).' },
                { aliases: ['--gpu-lanes'], value: '<auto|f|3>', defaultZh: 'auto', defaultEn: 'auto', descriptionZh: 'R 内核 lane 布局：auto；f = 4 条字节表 lane（64 KB shared）；3 = 2 条字节表 lane + 2 条 clmad（32 KB）。auto = 只要 64 KB shared 放得下就用 f', descriptionEn: 'R kernel lane configuration: auto, f (4 byte-table lanes, 64 KB shared), 3 (2 table lanes + 2 clmad, 32 KB). Auto = f wherever 64 KB of shared fits.' },
                { aliases: ['--gpu-tab'], value: '<auto|shared|l1>', defaultZh: 'auto', defaultEn: 'auto', descriptionZh: '常量乘法表放哪：auto（放得下就用 shared memory，RTX 4080 实测 +2.5%）、shared、l1', descriptionEn: 'Where the constant-multiply tables live: auto (shared memory on cards that fit it - RTX 4080 measured +2.5%), shared, or l1.' },
                { aliases: ['--gpu-batch-ms'], value: '<MS>', defaultZh: '80', defaultEn: '80', descriptionZh: '单次内核启动的目标时长。远低于 Windows TDR 超时（2 s）——超过它显示驱动会被重置', descriptionEn: 'Target wall time of a single kernel launch. Kept far below the Windows TDR timeout (2 s) - a launch that overruns it resets the display driver.' },
                { aliases: ['--check-gpu'], value: null, descriptionZh: '列出本构建能用的 GPU，然后退出', descriptionEn: 'List the GPUs this build can use, then exit.' },
                { aliases: ['--gpu-selftest'], value: null, descriptionZh: '对 noid_chain oracle 跑 GPU 共识自检，然后退出', descriptionEn: 'Run the GPU consensus self-test against the noid_chain oracle and exit.' },
                { aliases: ['--gpu-bench'], value: '<SECONDS>', descriptionZh: '离线 GPU 跑分：每个采样窗口 N 秒，5 个窗口取中位数', descriptionEn: 'Offline GPU benchmark: N seconds per sample window, 5 windows, median.' },
                { aliases: ['--gpu-launch-gate'], value: '<SECONDS>', descriptionZh: '发版闸门：满载跑 N 秒，要求每一次内核启动都不超过 --gpu-launch-cap，否则非零退出。Windows 在 2 s（TDR）重置显示驱动，撞上去没有第二次机会', descriptionEn: 'Release gate: drive the GPU under load for N seconds and require every single kernel launch to stay under --gpu-launch-cap; exits non-zero otherwise. Windows resets the display driver at 2 s (TDR) and a launch that hits it gets no second chance.' },
                { aliases: ['--gpu-launch-cap'], value: '<MS>', defaultZh: '500', defaultEn: '500', descriptionZh: '--gpu-launch-gate 的上限（毫秒）', descriptionEn: 'Ceiling for --gpu-launch-gate, in milliseconds.' },
                { aliases: ['--gpu-solvetest'], value: null, descriptionZh: '端到端出解测试：在人为放宽的目标下 GPU 与 CPU 各扫一小段 nonce，要求命中集合完全一致', descriptionEn: 'End-to-end solve test: scan a short nonce span against an artificially easy target on both GPU and CPU and require identical hit sets.' },
              ],
            },
            {
              id: 'other', titleZh: '其它', titleEn: 'Other',
              options: [
                { aliases: ['--verify-solution'], value: '<FIELDS_HEX> <TARGET_HEX> <NONCE_HEX>', descriptionZh: '独立复核一个已提交的解：--verify-solution <pow_fields_hex> <target_hex> <nonce_hex_le>。走官方全哈希路径而不是接受它的 midstate 引擎，两者不可能因共享同一个 bug 而一致', descriptionEn: 'Independently re-check a submitted solution: --verify-solution <pow_fields_hex> <target_hex> <nonce_hex_le>. Uses the official full-hash path, not the midstate engine that accepted it, so the two can never agree by sharing a bug.' },
                { aliases: ['-h', '--help'], value: null, descriptionZh: '打印帮助（-h 为摘要）', descriptionEn: 'Print help (see a summary with -h).' },
                { aliases: ['-V', '--version'], value: null, descriptionZh: '打印版本', descriptionEn: 'Print version.' },
              ],
            },
          ],
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
    /* ⛔ 2026-08-25 下线：Brisvia(BRVA) 项目方跑路，矿池与节点已全部停止。
       同 dom/noctari/velkar：前端全量遍历 coins，不看 enabled/order，只能整块移出；
       块注释保留完整配置，将来复活把注释去掉即可。
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
    */

    juno: {
      id: 'juno',
      enabled: true,
      order: 8,
      symbol: 'JUNO',
      name: 'Juno Cash',
      algo: 'rx/juno (RandomX)',
      color: '#b98a00',
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
      color: '#00a0a6',
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
        miner: 'ntmminer',
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

    midstate: {
      id: 'midstate',
      enabled: true,
      order: 15,
      symbol: 'MDS',
      name: 'midstate',
      algo: 'BLAKE3 VDF (midstate)',
      color: '#8451c9',
      logo: '/img/midstate.png',
      apiBase: '/api-mds',
      hashUnit: 'nonce/s',
      amountDecimals: { min: 0, max: 4 },
      amountScale: 1,
      binaryUnits: null,
      stratum: [
        { region: 'HK', host: 'hk.ntmminer.com', port: 13333, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'HK3', host: 'hk3.ntmminer.com', port: 13333, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'US', host: 'us.ntmminer.com', port: 13333, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'DE', host: 'eur.ntmminer.com', port: 13333, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'SG', host: 'sgp.ntmminer.com', port: 13333, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
        { region: 'CA', host: 'ca.ntmminer.com', port: 13333, mode: 'pool', label: { zh: 'VarDiff', en: 'VarDiff' }, tls: false },
      ],
      mining: {
        ntmAlgoFlag: 'midstate',
        miner: 'midstate',
        xmrigCompatible: false,
        xmrigAlgoFlag: null,
        modes: [
          {
            id: 'gpu',
            commandFlag: '--gpu-only',
            parameters: ['gpuOnly', 'gpuDevices'],
            description: {
              zh: 'midstate 是纯 GPU 币（零内存硬度，GPU 远快于 CPU），必须加 --gpu-only（隐含 --gpu）。多显卡用 --gpu-devices 0,1,2 指定，不写默认用全部 NVIDIA 卡。锄头 0% 抽水。',
              en: 'midstate is GPU-only (zero memory-hardness, the GPU far outpaces the CPU), so you must pass --gpu-only (which implies --gpu). For several cards use --gpu-devices 0,1,2; omit it to use every NVIDIA card. The miner takes a 0% dev fee.',
            },
          },
        ],
      },
      wallet: {
        addressPrefixes: [],
        example: {
          zh: '你的 64 位十六进制 MDS 地址（若钱包显示 72 位，只取前 64 位）',
          en: 'Your 64-hex MDS address (if your wallet shows 72 hex characters, use only the first 64)',
        },
      },
      settlement: {
        confirmations: 0,
        directPayout: true,
        noPayout: false,
        notice: {
          zh: 'midstate 是 coinbase 直付：矿池不打款，你挖到的 95% 在爆块那一刻直接进你的地址（矿池抽 5% 池费）。锄头本身 0% 抽水。',
          en: 'midstate is coinbase direct-pay: the pool never sends payouts. Your 95% lands in your address the moment a block is found (the pool keeps a 5% fee). The miner itself takes a 0% dev fee.',
        },
      },
      links: {
        site: 'https://midstate.cash',
        git: 'https://github.com/ciphernom/midstate',
      },
      description: {
        zh: 'midstate（MDS）是一条极简的「顺序时间」币：PoW = 对每个 nonce 串行迭代 100 万次 BLAKE3 压缩（类 VDF），零内存硬度、天然 GPU 友好、抗 ASIC。本池 PPLNS、池费 5%、coinbase 直付（爆块即到账）。用我们自研的 NTMminer-midstate（纯 GPU、0% 抽水），下载与详细教程见下方下载区。',
        en: 'midstate (MDS) is a minimalist sequential-time coin: PoW is one million iterated BLAKE3 compressions per nonce (VDF-like) with zero memory-hardness, naturally GPU-friendly and ASIC-resistant. This pool is PPLNS with a 5% fee and coinbase direct-pay (you are paid the instant a block is found). Mine it with our own NTMminer-midstate (GPU-only, 0% dev fee); download and the full tutorial are in the download section below.',
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

    /* ⛔ 2026-08-25 下线：Bitcoin09(09C) 项目方跑路，矿池与节点已全部停止。
       同 dom/noctari/velkar：前端全量遍历 coins，不看 enabled/order，只能整块移出；
       块注释保留完整配置，将来复活把注释去掉即可。
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
    */

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
