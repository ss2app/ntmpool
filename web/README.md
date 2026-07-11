# NTM 矿池前端（www.ntmminer.com）

纯静态 SPA，数据全部来自 NTMPool 公共 API（miningcore 形状 7 端点）。中英双语（localStorage `ntm_lang`，默认跟浏览器语言）。

## 架构（2026-07-11 上线）

```
矿工浏览器
  → Cloudflare 橙云 (www.ntmminer.com / *.ntmminer.com)
  → 前端机 134.185.123.52 (Oracle, Ubuntu 24.04, SSH :8422 同项目 id_rsa)
      nginx: /var/www/ntmminer 静态站
             /api/ → proxy 127.0.0.1:14000/api/ (10s proxy_cache + 20r/s limit_req + CF 真实 IP)
             自签证书 /etc/nginx/ssl/ntmminer.{crt,key} (SAN: ntmminer.com + *.ntmminer.com)
      autossh systemd `ntmpool-apitunnel.service`:
             127.0.0.1:14000 → 池机 103.80.18.140 的 127.0.0.1:14000
             受限 key /root/.ssh/ntmpool_api_tunnel
             （池机 authorized_keys: restrict,port-forwarding,permitopen="127.0.0.1:14000"，
               备份 authorized_keys.bak-ntmminerweb-20260711）
  → 池机 NTMPool 公共 API 127.0.0.1:14000（矿池真实 IP 对外不可见）
```

- `/metrics` 与管理面**不经 nginx 暴露**（只反代 `/api/`）。
- Cloudflare SSL 模式建议 **Full**（源站自签）。
- 80/443 云安全组已开（实测），前端机 ufw inactive。

## 文件

| 文件 | 作用 |
|---|---|
| `index.html` | 壳（nav/footer/脚本引用），SPA 路由 fallback 由 nginx `try_files /index.html` |
| `js/config.js` | **唯一编辑点**：品牌（NTM 英文全称、GitHub 仓库）+ 币种注册表（stratum 入口、官方链接、地址前缀等静态元信息） |
| `js/i18n.js` | 中英文案字典 |
| `js/app.js` | 路由 + 渲染 + ApexCharts 图表 |
| `css/style.css` | 深色主题（dataviz 已验证参考色板） |
| `vendor/apexcharts.min.js` | 自托管 ApexCharts 3.54.1（服务器上有，仓库不存） |

## 加新币（币种迁入 NTMPool 后）

1. `js/config.js` 的 `coins` 里加一段（key = NTMPool 池 id），填 symbol/algo/stratum 公网入口/官方链接/地址前缀。
2. 上传：`scp js/config.js root@134.185.123.52:/var/www/ntmminer/js/`。
3. 首页卡片、币种页全自动出现（数据驱动自 `/api/pools`）。

## 更新部署

```bash
node --check js/app.js   # 先语法检查
# ⚠ 改了 js/css 必须先 bump index.html 里的版本号 query（?v=YYYYMMDDx），
#   否则 Cloudflare 边缘缓存继续发旧版（详见 _knowledge/pitfalls/cloudflare缓存js-版本号cache-busting.md）
scp -i <id_rsa> -P 8422 index.html root@134.185.123.52:/var/www/ntmminer/
scp -i <id_rsa> -P 8422 js/*.js    root@134.185.123.52:/var/www/ntmminer/js/
scp -i <id_rsa> -P 8422 css/*.css  root@134.185.123.52:/var/www/ntmminer/css/
scp -i <id_rsa> -P 8422 img/*      root@134.185.123.52:/var/www/ntmminer/img/   # 币 logo（config.js 的 logo 字段引用）
```

nginx 已配缓存头兜底：js/css `max-age=300`、HTML `no-cache`。

## 已知口径 / 约束

- **只放真实数据**：全网算力/难度来自节点（经池 API `networkStats`）、池算力=实际接受 share（`poolStats.poolHashrate`）；页面不做估算。
- 算力曲线来自 `/performance`（10min 桶、24h、内存态）——**矿池进程重启曲线清零**，持久化时序（`poolstats` 表 + HashrateSampler）是核心侧待补项。
- 图表丢掉当前未采满的 10 分钟桶（否则末尾假跌 0）。
- 矿工自查限速 60 次/分/IP 在池端按 IP 计——经隧道后池端只见 127.0.0.1，等于全站共享限速；nginx 已按真实 IP 20r/s 限流 + 10s 缓存缓解。
- 币卡「池费率」直读 API（dragonx=3%）；「0 手续费」宣传语指 **NTMminer 0% 开发者抽水**，两者别混。
- **SOLO 全数据驱动**：API ports 里有 `solo:true` 端口 → 币卡出 SOLO 徽标；`soloFeePercent` 存在 → 各处费率自动双显；config.js stratum 条目加 `mode:'solo'` → 教程页接入点表标 SOLO+说明。dragonx 现役：`hk.dragonx.cc:7777`（SOLO 3%）。
- 币 logo：config.js 币种加 `logo:'/img/xxx.png'`（透明 PNG，渲染在**黑色圆底**上）；没有 logo 自动回退彩色字母圆标。
