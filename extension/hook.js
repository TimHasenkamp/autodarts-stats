// hook.js – laeuft in der MAIN world der Seite (document_start).
// Wrappt fetch, XMLHttpRequest und WebSocket, kopiert JSON-Antworten und
// reicht sie per postMessage an relay.js weiter. Zusaetzlich werden Zaehler
// gefuehrt, damit in den Extension-Optionen sichtbar ist, ob der Hook
// ueberhaupt greift. Keine Logik, nur Rohdaten.
(() => {
  if (window.__adstatsHooked) return;
  window.__adstatsHooked = true;

  const SRC = 'adstats-hook';
  const MAX_BODY = 4 * 1024 * 1024;
  // Grobfilter; Feinfilter (konfigurierbar) sitzt in relay.js.
  // Die App spricht mit api.autodarts.com sowie play.ws.autodarts.com und
  // boards.ws.autodarts.com, aelter auch ueber autodarts.io.
  const HOST = /autodarts\.(com|io)|localhost|127\.0\.0\.1/;

  const stats = {
    startedAt: Date.now(),
    fetchTotal: 0, fetchHost: 0, fetchSent: 0,
    xhrTotal: 0, xhrHost: 0, xhrSent: 0,
    wsConn: 0, wsMsg: 0, wsSent: 0,
    lastUrl: '',
  };
  let dirty = true;

  function send(msg) {
    try {
      msg.src = SRC;
      window.postMessage(msg, window.location.origin);
    } catch (_) { /* ignorieren */ }
  }

  function post(kind, url, body) {
    if (typeof body !== 'string' || body.length === 0 || body.length > MAX_BODY) return;
    stats.lastUrl = String(url).slice(0, 200);
    dirty = true;
    send({ kind, url: String(url), ts: Date.now(), body });
  }

  send({ kind: 'hello', href: String(location.href) });
  setInterval(() => {
    if (!dirty) return;
    dirty = false;
    send({ kind: 'stats', stats: JSON.parse(JSON.stringify(stats)) });
  }, 2000);

  // ---- fetch ----
  const origFetch = window.fetch;
  window.fetch = function (input, init) {
    const p = origFetch.apply(this, arguments);
    let url = '';
    try {
      url = typeof input === 'string' ? input : (input && input.url) || '';
    } catch (_) {}
    stats.fetchTotal++;
    dirty = true;
    if (!HOST.test(url)) return p;
    stats.fetchHost++;
    return p.then((res) => {
      try {
        const ct = (res.headers && res.headers.get('content-type')) || '';
        if (res.ok && ct.indexOf('json') !== -1) {
          stats.fetchSent++;
          res.clone().text().then((t) => post('fetch', url, t)).catch(() => {});
        }
      } catch (_) {}
      return res;
    });
  };

  // ---- XMLHttpRequest ----
  const origOpen = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function (method, url) {
    try {
      stats.xhrTotal++;
      dirty = true;
      if (HOST.test(String(url))) {
        stats.xhrHost++;
        this.addEventListener('load', function () {
          try {
            const ct = this.getResponseHeader('content-type') || '';
            const txt = this.responseType === '' || this.responseType === 'text';
            if (this.status >= 200 && this.status < 300 && ct.indexOf('json') !== -1 && txt) {
              stats.xhrSent++;
              post('fetch', String(url), this.responseText);
            }
          } catch (_) {}
        });
      }
    } catch (_) {}
    return origOpen.apply(this, arguments);
  };

  // ---- WebSocket ----
  const OrigWS = window.WebSocket;
  function WrappedWS(url, protocols) {
    const ws = protocols === undefined ? new OrigWS(url) : new OrigWS(url, protocols);
    try {
      stats.wsConn++;
      dirty = true;
      ws.addEventListener('message', (ev) => {
        stats.wsMsg++;
        dirty = true;
        if (typeof ev.data === 'string' && HOST.test(String(ws.url))) {
          stats.wsSent++;
          post('ws', ws.url, ev.data);
        }
      });
    } catch (_) {}
    return ws;
  }
  WrappedWS.prototype = OrigWS.prototype;
  for (const k of ['CONNECTING', 'OPEN', 'CLOSING', 'CLOSED']) WrappedWS[k] = OrigWS[k];
  window.WebSocket = WrappedWS;

  // ---- EventSource (Server-Sent Events), falls genutzt ----
  try {
    const OrigES = window.EventSource;
    if (OrigES) {
      function WrappedES(url, cfg) {
        const es = cfg === undefined ? new OrigES(url) : new OrigES(url, cfg);
        try {
          if (HOST.test(String(url))) {
            es.addEventListener('message', (ev) => {
              if (typeof ev.data === 'string') post('ws', String(url), ev.data);
            });
          }
        } catch (_) {}
        return es;
      }
      WrappedES.prototype = OrigES.prototype;
      window.EventSource = WrappedES;
    }
  } catch (_) {}
})();
