const DEFAULTS = {
  backendUrl: '', apiKey: '', boardName: '',
  fetchPattern: 'autodarts\\.(com|io)/(gs|as|bs)/',
  wsPattern: 'autodarts\\.matches',
  excludePattern: '/users?/|/auth|login|token|keycloak',
  showOverlay: true,
  captureAll: false,
};
const $ = (id) => document.getElementById(id);

async function load() {
  const cfg = await chrome.storage.sync.get(DEFAULTS);
  for (const k of Object.keys(DEFAULTS)) {
    if (typeof DEFAULTS[k] === 'boolean') $(k).checked = !!cfg[k];
    else $(k).value = cfg[k] || '';
  }
  refreshStatus();
}

// Ergaenzt ein fehlendes Schema: "localhost:8080" -> "http://localhost:8080"
function normalizeBackendUrl(raw) {
  let u = String(raw || '').trim().replace(/\/+$/, '');
  if (!u) return '';
  if (!/^https?:\/\//i.test(u)) u = 'http://' + u.replace(/^\/+/, '');
  try {
    new URL(u);
  } catch (_) {
    return '';
  }
  return u;
}

async function ensureHostPermission(url) {
  try {
    const origin = new URL(url).origin + '/*';
    const has = await chrome.permissions.contains({ origins: [origin] });
    if (has) return true;
    return await chrome.permissions.request({ origins: [origin] });
  } catch (_) {
    return false;
  }
}

$('save').addEventListener('click', async () => {
  const cfg = {};
  for (const k of Object.keys(DEFAULTS)) cfg[k] = typeof DEFAULTS[k] === 'boolean' ? $(k).checked : $(k).value.trim();
  cfg.backendUrl = normalizeBackendUrl(cfg.backendUrl);
  if ($('backendUrl').value.trim() && !cfg.backendUrl) {
    $('test').textContent = 'Die Backend-URL ist keine gültige Adresse.';
    $('test').className = 'err';
    return;
  }
  $('backendUrl').value = cfg.backendUrl;
  const granted = cfg.backendUrl ? await ensureHostPermission(cfg.backendUrl) : true;
  await chrome.storage.sync.set(cfg);
  $('test').textContent = granted ? 'Gespeichert.' : 'Gespeichert, aber Zugriff auf die Backend-URL wurde nicht erlaubt.';
  $('test').className = granted ? 'ok' : 'err';
  chrome.runtime.sendMessage({ type: 'flush' }, () => refreshStatus());
});

$('testBtn').addEventListener('click', async () => {
  const backendUrl = normalizeBackendUrl($('backendUrl').value);
  const apiKey = $('apiKey').value.trim();
  if (!backendUrl || !apiKey) { $('test').textContent = 'Backend-URL und API-Key eintragen.'; $('test').className = 'err'; return; }
  $('backendUrl').value = backendUrl;
  await ensureHostPermission(backendUrl);
  $('test').textContent = 'Teste …'; $('test').className = '';
  chrome.runtime.sendMessage({ type: 'test', backendUrl, apiKey }, (r) => {
    if (!r) { $('test').textContent = 'Keine Antwort vom Hintergrundskript.'; $('test').className = 'err'; return; }
    if (r.ok) {
      $('test').textContent = 'OK, Board erkannt: ' + r.body;
      $('test').className = 'ok';
    } else if (r.status === 401) {
      $('test').textContent = 'Der Server antwortet, lehnt aber den API-Key ab (401). '
        + 'Key neu erzeugen mit: ./autodarts-stats board add "Wohnzimmer"';
      $('test').className = 'err';
    } else {
      $('test').textContent = 'Fehler (' + r.status + '): ' + r.body;
      $('test').className = 'err';
    }
  });
});

$('flush').addEventListener('click', () => chrome.runtime.sendMessage({ type: 'flush' }, () => refreshStatus()));
$('clear').addEventListener('click', () => chrome.runtime.sendMessage({ type: 'clearQueue' }, () => refreshStatus()));
$('reset').addEventListener('click', () => chrome.runtime.sendMessage({ type: 'resetDiag' }, () => refreshStatus()));

function el(tag, opts) {
  const n = document.createElement(tag);
  if (opts && opts.text !== undefined) n.textContent = opts.text;
  if (opts && opts.cls) n.className = opts.cls;
  return n;
}

function tableRow(label, value) {
  const tr = el('tr');
  tr.append(el('td', { text: label }), el('td', { text: String(value) }));
  return tr;
}

// Baut die Diagnoseanzeige aus DOM-Knoten. Werte von der Webseite (URLs)
// werden ausschliesslich per textContent gesetzt, nie per innerHTML.
function renderDiag(s) {
  const d = s.diag;
  const box = $('diagBox');
  box.replaceChildren();

  if (!d || !d.hookAlive) {
    box.append(el('p', { cls: 'err', text: 'Der Hook meldet sich nicht.' }));
    box.append(el('p', {
      cls: 'hint',
      text: 'Auf play.autodarts.io läuft das Skript der Extension nicht. Ist ein Tab mit '
        + 'play.autodarts.io offen und wurde er nach dem Installieren neu geladen? '
        + 'In Firefox zusätzlich unter about:addons → Your Darts Collector → Berechtigungen '
        + 'den Zugriff auf play.autodarts.io erlauben.',
    }));
    return;
  }

  const h = d.hook || {};
  const seen = (h.fetchTotal || 0) + (h.xhrTotal || 0) + (h.wsMsg || 0);
  let cls = 'ok';
  let text = 'Erfassung läuft.';
  if (seen === 0) {
    cls = 'warn';
    text = 'Der Hook läuft, hat aber noch keinen einzigen Netzwerkaufruf gesehen. '
      + 'Spiel ein paar Würfe und sieh dann erneut nach.';
  } else if (d.received === 0) {
    cls = 'warn';
    text = 'Aufrufe werden gesehen, aber keiner geht an autodarts.io oder liefert JSON. '
      + 'Die Daten kommen dann über einen anderen Weg.';
  } else if (d.passed === 0) {
    cls = 'warn';
    text = 'Daten werden erfasst, aber die Filter werfen alles weg. '
      + 'Setz den Haken bei „Alles erfassen (Debug)“ und lade die Seite neu.';
  }
  box.append(el('p', { cls, text }));

  const table = el('table', { cls: 'diag' });
  table.append(
    tableRow('Hook aktiv seit', h.startedAt ? new Date(h.startedAt).toLocaleTimeString() : '–'),
    tableRow('fetch gesamt / autodarts / gelesen', `${h.fetchTotal || 0} / ${h.fetchHost || 0} / ${h.fetchSent || 0}`),
    tableRow('XHR gesamt / autodarts / gelesen', `${h.xhrTotal || 0} / ${h.xhrHost || 0} / ${h.xhrSent || 0}`),
    tableRow('WebSocket Verb. / Nachrichten / gelesen', `${h.wsConn || 0} / ${h.wsMsg || 0} / ${h.wsSent || 0}`),
    tableRow('an Filter übergeben', d.received),
    tableRow('durchgelassen', d.passed),
    tableRow('verworfen (Muster / Ausschluss)', `${d.droppedFilter} / ${d.droppedExclude}`),
    tableRow('an Server gesendet', s.sentTotal),
    tableRow('in Queue', s.queued),
  );
  box.append(table);

  for (const [label, url] of [['Zuletzt durchgelassen:', d.lastPassedUrl], ['Zuletzt verworfen:', d.lastDroppedUrl]]) {
    if (!url) continue;
    const p = el('p', { cls: 'hint', text: label });
    p.append(el('br'), el('code', { text: url }));
    box.append(p);
  }
}

function refreshStatus() {
  chrome.runtime.sendMessage({ type: 'status' }, (s) => {
    if (chrome.runtime.lastError || !s) return;
    renderDiag(s);
    const lines = [];
    if (s.lastOk) lines.push('Letzter Erfolg: ' + new Date(s.lastOk).toLocaleTimeString());
    if (s.lastError) lines.push('Letzter Fehler: ' + s.lastError);
    $('status').textContent = lines.join('\n');
    $('status').className = s.lastError ? 'err' : '';
  });
}

load();
setInterval(refreshStatus, 2000);
