/* 事故公告弹窗 —— 2026-08-02（b 版：SPA 内导航也会重新弹）
 *
 * 背景：stratum 中转线路持续遭 DDoS，us 线路已不可用；已上线三条带流量清洗的新线路。
 *
 * 两条硬约束（改这个文件前先读）：
 *  ① 站点 CSP = script-src 'self' / style-src 'self' —— 内联 <script>/<style> 会被浏览器直接拦掉，
 *     所以本公告必须是独立 .js + 独立 .css，不能塞进 index.html。
 *  ② 语言不自己存：读 app 的 localStorage['ntm_lang']，并监听 app 派发的 'ntm:language'
 *     事件跟随切换 —— 与右上角语言按钮保持一致，不引入第二套语言状态。
 *
 * ★ b 版修的问题（a 版矿工实际反馈）：站点是 SPA，点导航不会重新加载页面，
 *   所以 a 版只在首次 DOMContentLoaded 弹一次 —— 点"首页"、点进币种页都不弹，必须 F5 才弹。
 *   b 版包装 history.pushState/replaceState 并监听 popstate：**pathname 变化即重新弹**。
 *   只认 pathname：币页内切 tab 走的是 hash，不该反复弹。
 *
 * 行为：首次加载弹；此后每次切换到不同路径再弹一次；关闭只对当前路径当次有效。
 * 撤下公告：删掉 index.html 里 notice.*.css / notice.*.js 两行引用即可，SPA 零依赖。
 */
(function () {
  'use strict';

  var LANG_KEY = 'ntm_lang';

  function currentLang() {
    try {
      var stored = localStorage.getItem(LANG_KEY);
      if (stored === 'zh' || stored === 'en') return stored;
    } catch (e) { /* storage 不可用时退到浏览器语言 */ }
    return (navigator.language || '').toLowerCase().indexOf('zh') === 0 ? 'zh' : 'en';
  }

  // 三条新线路。端口按币分开列，矿工可直接照着改自己的命令行。
  var RELAYS = [
    { zh: '德国',     en: 'Germany',   host: 'eur.ntmminer.com' },
    { zh: '新加坡',   en: 'Singapore', host: 'sgp.ntmminer.com' },
    { zh: '加拿大',   en: 'Canada',    host: 'ca.ntmminer.com'  }
  ];

  var TEXT = {
    zh: {
      title: '⚠ 接入线路正在遭受持续 DDoS 攻击',
      p1: '我们的 stratum 中转线路持续遭受 DDoS 攻击，原美国线路（<b>us.ntmminer.com</b>）目前已不可用。',
      p2: '<b>矿池、节点与账本全部正常运行，没有丢失任何 share，收益也不受影响。</b>被攻击的只是矿工连接矿池的中转线路。',
      blockT: '已新增三条带 DDoS 流量清洗的线路，可直接使用',
      ports: '端口：<span class="mono">BRVA 5541</span>（VarDiff）/ <span class="mono">5542</span>（SOLO）；' +
             '<span class="mono">09C 8344</span>（VarDiff）/ <span class="mono">8345</span>（SOLO）',
      p3: '在币种页面的「接入」标签里选择线路，页面会自动生成完整命令；也可以直接把你现有命令里的主机名换成上面任意一个，其余参数不用动。',
      hint: '香港线路（hk / hk2 / hk3）目前仍然可用。三条新线路与原线路连的是同一个矿池、同一个账本，收益完全一样，只是网络路径不同。',
      ok: '知道了',
      close: '关闭'
    },
    en: {
      title: '⚠ Our relay entries are under sustained DDoS attack',
      p1: 'Our stratum relay routes are under sustained DDoS attack. The former US entry (<b>us.ntmminer.com</b>) is currently unreachable.',
      p2: '<b>The pool, the node and the ledger are all running normally. No shares have been lost and earnings are unaffected.</b> Only the relay hop between miners and the pool is affected.',
      blockT: 'Three new routes with always-on DDoS scrubbing are live — use any of them',
      ports: 'Ports: <span class="mono">BRVA 5541</span> (VarDiff) / <span class="mono">5542</span> (SOLO); ' +
             '<span class="mono">09C 8344</span> (VarDiff) / <span class="mono">8345</span> (SOLO)',
      p3: 'Pick a route on the Connect tab of any coin page and the full command is generated for you. You can also just swap the hostname in your existing command for one of the above — every other argument stays the same.',
      hint: 'The Hong Kong routes (hk / hk2 / hk3) remain available. All routes reach the same pool and the same ledger — earnings are identical, only the network path differs.',
      ok: 'Got it',
      close: 'Close'
    }
  };

  function build() {
    var mask = document.createElement('div');
    mask.className = 'ntc-mask';
    mask.id = 'ntc-outage';

    var card = document.createElement('div');
    card.className = 'ntc-card';
    card.setAttribute('role', 'dialog');
    card.setAttribute('aria-modal', 'true');
    card.setAttribute('aria-labelledby', 'ntc-title');

    var head = document.createElement('div');
    head.className = 'ntc-head';
    var h2 = document.createElement('h2');
    h2.id = 'ntc-title';
    var x = document.createElement('button');
    x.type = 'button';
    x.className = 'ntc-x';
    x.textContent = '✕';
    head.appendChild(h2);
    head.appendChild(x);

    var body = document.createElement('div');
    body.className = 'ntc-body';
    var p1 = document.createElement('p');
    var p2 = document.createElement('p');
    p2.className = 'ntc-ok';
    var block = document.createElement('div');
    block.className = 'ntc-block';
    var blockT = document.createElement('div');
    blockT.className = 'ntc-block-t';
    var eps = document.createElement('div');
    eps.className = 'ntc-eps';
    var locs = [];
    RELAYS.forEach(function (r) {
      var row = document.createElement('div');
      row.className = 'ntc-ep';
      var loc = document.createElement('span');
      loc.className = 'ntc-ep-loc';
      var host = document.createElement('span');
      host.className = 'ntc-ep-host';
      host.textContent = r.host;
      row.appendChild(loc);
      row.appendChild(host);
      eps.appendChild(row);
      locs.push({ el: loc, relay: r });
    });
    var ports = document.createElement('div');
    ports.className = 'ntc-ports';
    block.appendChild(blockT);
    block.appendChild(eps);
    block.appendChild(ports);

    var p3 = document.createElement('p');
    var hint = document.createElement('p');
    hint.className = 'ntc-hint';
    var foot = document.createElement('div');
    foot.className = 'ntc-foot';
    var ok = document.createElement('button');
    ok.type = 'button';
    ok.className = 'ntc-ok-btn';
    foot.appendChild(ok);

    body.appendChild(p1);
    body.appendChild(p2);
    body.appendChild(block);
    body.appendChild(p3);
    body.appendChild(hint);
    body.appendChild(foot);
    card.appendChild(head);
    card.appendChild(body);
    mask.appendChild(card);

    function paint() {
      var s = TEXT[currentLang()] || TEXT.zh;
      h2.textContent = s.title;
      x.setAttribute('aria-label', s.close);
      x.title = s.close;
      p1.innerHTML = s.p1;
      p2.innerHTML = s.p2;
      blockT.textContent = s.blockT;
      ports.innerHTML = s.ports;
      p3.textContent = s.p3;
      hint.textContent = s.hint;
      ok.textContent = s.ok;
      var lang = currentLang();
      locs.forEach(function (item) {
        item.el.textContent = lang === 'zh' ? item.relay.zh : item.relay.en;
      });
    }

    function close() { mask.hidden = true; }
    function show() { paint(); mask.hidden = false; }

    // keydown 常驻监听：show/close 会反复发生，挂一次、靠 hidden 判断即可
    document.addEventListener('keydown', function (e) {
      if ((e.key === 'Escape' || e.key === 'Esc') && !mask.hidden) close();
    });
    x.addEventListener('click', close);
    ok.addEventListener('click', close);
    mask.addEventListener('click', function (e) { if (e.target === mask) close(); });
    window.addEventListener('ntm:language', function () { if (!mask.hidden) paint(); });

    // ── SPA 路由感知：pathname 变了就再弹一次 ──────────────────────────────
    // 站点用 history.pushState/replaceState 导航（app.js navigate()），
    // 不触发 load/DOMContentLoaded，所以必须包装这两个方法才感知得到。
    var lastPath = location.pathname;
    function onNavigated() {
      if (location.pathname === lastPath) return; // 同页切 tab(hash) 不重复弹
      lastPath = location.pathname;
      show();
    }
    ['pushState', 'replaceState'].forEach(function (m) {
      var orig = history[m];
      if (typeof orig !== 'function') return;
      history[m] = function () {
        var r = orig.apply(this, arguments);
        // 让 app 先把 URL 改完再判断
        setTimeout(onNavigated, 0);
        return r;
      };
    });
    window.addEventListener('popstate', function () { setTimeout(onNavigated, 0); });

    show();
    document.body.appendChild(mask);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', build);
  } else {
    build();
  }
})();
