# BRC 矿池面板 `index.html` 快照

⚠ **这份 HTML 不在前端机**，它在**矿池机 154.219.120.65** 的 `/var/www/brc-pool/index.html`，
由 nginx 直接落盘 serve（**改它不用重启 brc-pool**）。

08-31 就是因为忘了它：主站 `config.js` 的退役中转机清干净了，
这份面板里还留着 sgp / ca / hk3 三张入口卡在对外公示。
**以后改中转入口公示，前端机和矿池机两台都要扫。**

`cf-cache-status: DYNAMIC` + `no-cache` ⇒ 改完即时生效，不用清 CF 缓存。
回滚：154 上 `index.html.bak-pre-relaydrop-20260831`。
