/* NTM pool frontend — 唯一编辑点：品牌 + 币种注册表。
 * 动态数据一律来自 /api（NTMPool 公共 API），此处只放静态元信息。 */
window.NTM_CONFIG = {
  brand: {
    // NTM 英文全称（改这里即可全站生效）
    expansionEn: 'Next-Tier Mining',
    githubMiner: 'https://github.com/scashcc/NTMminer-Multi',
    githubMinerLabel: 'github.com/scashcc/NTMminer-Multi',
    minerReleaseApi: 'https://api.github.com/repos/scashcc/NTMminer-Multi/releases/latest',
    // GitHub API 拉不到时的兜底版本（页面会先显示这个，再被实时版本覆盖）
    minerVersionFallback: 'v1.13.0',
  },
  api: { base: '/api', refreshMs: 20000 },
  // key = NTMPool 池 id（/api/pools 里的 id）。只有出现在 /api/pools 里的币才会显示为在线。
  coins: {
    dragonx: {
      symbol: 'DRGX',
      name: 'DragonX',
      algo: 'rx/dragonx',
      color: '#e34948',
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
  },
};
