// relay.js – isolated world. Nimmt Nachrichten von hook.js entgegen,
// filtert nach konfigurierten Mustern, reicht sie an bg.js weiter und
// meldet Diagnosezahlen. Zeigt ausserdem aktive Check-ins als Overlay.
(() => {
  const DEFAULTS = {
    fetchPattern: 'autodarts\\.(com|io)/(gs|as|bs)/',
    wsPattern: '',
    excludePattern: '/users?/|/auth|login|token|keycloak',
    showOverlay: true,
    captureAll: false,
  };
  const cfg = { ...DEFAULTS };
  let fetchRe, wsRe, excludeRe;

  const diag = {
    hookAlive: false,
    hookHref: '',
    hook: null,
    received: 0,
    passed: 0,
    droppedFilter: 0,
    droppedExclude: 0,
    lastPassedUrl: '',
    lastDroppedUrl: '',
    updatedAt: 0,
  };

  function compile() {
    try { fetchRe = new RegExp(cfg.fetchPattern || DEFAULTS.fetchPattern, 'i'); } catch (_) { fetchRe = new RegExp(DEFAULTS.fetchPattern, 'i'); }
    try { wsRe = cfg.wsPattern ? new RegExp(cfg.wsPattern, 'i') : null; } catch (_) { wsRe = null; }
    try { excludeRe = cfg.excludePattern ? new RegExp(cfg.excludePattern, 'i') : null; } catch (_) { excludeRe = null; }
  }
  compile();

  chrome.storage.sync.get(DEFAULTS, (stored) => {
    Object.assign(cfg, stored);
    compile();
  });
  chrome.storage.onChanged.addListener((changes, area) => {
    if (area !== 'sync') return;
    for (const k of Object.keys(DEFAULTS)) if (changes[k]) cfg[k] = changes[k].newValue;
    compile();
  });

  let diagTimer = null;
  function reportDiag() {
    if (diagTimer) return;
    diagTimer = setTimeout(() => {
      diagTimer = null;
      diag.updatedAt = Date.now();
      try {
        chrome.runtime.sendMessage({ type: 'diag', diag }, () => void chrome.runtime.lastError);
      } catch (_) {}
    }, 500);
  }

  window.addEventListener('message', (ev) => {
    if (ev.source !== window || !ev.data || ev.data.src !== 'adstats-hook') return;
    const d = ev.data;

    if (d.kind === 'hello') {
      diag.hookAlive = true;
      diag.hookHref = String(d.href || '').slice(0, 200);
      reportDiag();
      return;
    }
    if (d.kind === 'stats') {
      diag.hookAlive = true;
      diag.hook = d.stats;
      reportDiag();
      return;
    }
    if (d.kind !== 'fetch' && d.kind !== 'ws') return;
    if (typeof d.body !== 'string') return;

    diag.received++;
    const url = String(d.url || '');
    if (!cfg.captureAll) {
      if (excludeRe && excludeRe.test(url)) {
        diag.droppedExclude++;
        diag.lastDroppedUrl = url.slice(0, 200);
        reportDiag();
        return;
      }
      if (d.kind === 'fetch' && !fetchRe.test(url)) {
        diag.droppedFilter++;
        diag.lastDroppedUrl = url.slice(0, 200);
        reportDiag();
        return;
      }
      if (d.kind === 'ws' && wsRe && !wsRe.test(d.body.slice(0, 1024))) {
        diag.droppedFilter++;
        reportDiag();
        return;
      }
    }
    diag.passed++;
    diag.lastPassedUrl = url.slice(0, 200);
    reportDiag();
    try {
      chrome.runtime.sendMessage(
        { type: 'event', event: { kind: d.kind, url, ts: d.ts, body: d.body } },
        () => void chrome.runtime.lastError,
      );
    } catch (_) { /* Service Worker nicht erreichbar, z.B. nach Update */ }
  });

  // ---- Check-in-Overlay: zeigt eingestempelte Spieler mit ihrem Spielnamen ----
  let host = null;
  let lastKey = '';
  function render(list) {
    if (!cfg.showOverlay || !Array.isArray(list) || list.length === 0) {
      if (host) { host.remove(); host = null; lastKey = ''; }
      return;
    }
    const key = list.map((c) => c.game_name).join('|');
    if (host && key === lastKey) return;
    lastKey = key;
    if (!host) {
      host = document.createElement('div');
      host.id = 'adstats-overlay';
      host.style.cssText = 'position:fixed;right:12px;bottom:12px;z-index:2147483647;';
      host.attachShadow({ mode: 'open' });
      (document.body || document.documentElement).appendChild(host);
    }
    const root = host.shadowRoot;
    root.innerHTML = `
      <style>
        .box{font:13px system-ui,sans-serif;background:#1b1f24;color:#e9ecef;border:1px solid #2b3138;border-radius:10px;padding:8px 10px;box-shadow:0 4px 16px rgba(0,0,0,.4);max-width:260px}
        .t{font-size:11px;color:#9aa3ae;text-transform:uppercase;letter-spacing:.03em;margin-bottom:4px;display:flex;justify-content:space-between;gap:8px}
        .row{display:flex;align-items:center;gap:8px;padding:3px 0}
        code{font-size:15px;font-weight:700;user-select:all}
        button{font:12px system-ui;background:#3fbf78;color:#0c1a12;border:0;border-radius:6px;padding:4px 8px;cursor:pointer}
        .x{background:transparent;color:#9aa3ae;padding:0 4px;font-size:14px}
      </style>
      <div class="box"><div class="t"><span>Your Darts · Eingecheckt</span><button class="x" title="Ausblenden">×</button></div></div>`;
    const box = root.querySelector('.box');
    root.querySelector('.x').addEventListener('click', () => { host.remove(); host = null; lastKey = 'hidden'; });
    for (const c of list) {
      const row = document.createElement('div');
      row.className = 'row';
      const code = document.createElement('code');
      code.textContent = c.game_name;
      const btn = document.createElement('button');
      btn.textContent = 'Kopieren';
      btn.addEventListener('click', () => {
        navigator.clipboard.writeText(c.game_name).then(() => { btn.textContent = 'Kopiert'; setTimeout(() => (btn.textContent = 'Kopieren'), 1500); }).catch(() => {});
      });
      row.append(code, btn);
      box.appendChild(row);
    }
  }
  function poll() {
    if (lastKey === 'hidden') return;
    try {
      chrome.runtime.sendMessage({ type: 'checkins' }, (r) => {
        if (chrome.runtime.lastError || !r) return;
        render(r.list || []);
      });
    } catch (_) {}
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', poll);
  else poll();
  setInterval(poll, 20000);
})();
