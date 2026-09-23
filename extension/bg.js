// bg.js – Service Worker. Sammelt Events, persistiert eine Retry-Queue und
// sendet Batches an {BACKEND_URL}/api/ingest.
const QUEUE_KEY = 'queue';
const MAX_QUEUE = 500;
const BATCH = 50;
const FLUSH_DELAY_MS = 1000;
const RETRY_ALARM = 'adstats-retry';

let queue = null;      // Array von Events
let flushTimer = null;
let sending = false;
let lastError = '';
let lastOk = 0;
let sentTotal = 0;

async function loadQueue() {
  if (queue) return queue;
  const r = await chrome.storage.local.get({ [QUEUE_KEY]: [] });
  queue = Array.isArray(r[QUEUE_KEY]) ? r[QUEUE_KEY] : [];
  return queue;
}
async function saveQueue() {
  await chrome.storage.local.set({ [QUEUE_KEY]: queue });
  updateBadge();
}
function updateBadge() {
  const n = queue ? queue.length : 0;
  chrome.action.setBadgeText({ text: n ? String(n) : '' });
  chrome.action.setBadgeBackgroundColor({ color: lastError ? '#c62828' : '#2e7d32' });
}

async function getConfig() {
  return chrome.storage.sync.get({ backendUrl: '', apiKey: '', boardName: '' });
}

async function enqueue(ev) {
  await loadQueue();
  queue.push(ev);
  if (queue.length > MAX_QUEUE) queue.splice(0, queue.length - MAX_QUEUE);
  await saveQueue();
  scheduleFlush();
}

function scheduleFlush(delay = FLUSH_DELAY_MS) {
  if (flushTimer) return;
  flushTimer = setTimeout(() => { flushTimer = null; flush(); }, delay);
}

async function flush() {
  if (sending) return;
  await loadQueue();
  if (queue.length === 0) return;
  const cfg = await getConfig();
  if (!cfg.backendUrl || !cfg.apiKey) {
    lastError = 'Backend-URL oder API-Key fehlt';
    updateBadge();
    return;
  }
  sending = true;
  try {
    while (queue.length > 0) {
      const batch = queue.slice(0, BATCH);
      const res = await fetch(cfg.backendUrl.replace(/\/+$/, '') + '/api/ingest', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + cfg.apiKey },
        body: JSON.stringify({ board: cfg.boardName, events: batch }),
      });
      if (res.status === 401 || res.status === 403) {
        lastError = 'API-Key abgelehnt (' + res.status + ')';
        break;
      }
      if (res.status === 413 || res.status === 400) {
        // Batch ist kaputt/zu gross: verwerfen statt ewig zu haengen.
        queue.splice(0, batch.length);
        await saveQueue();
        lastError = 'Server hat Batch abgelehnt (' + res.status + ')';
        continue;
      }
      if (!res.ok) {
        lastError = 'HTTP ' + res.status;
        break;
      }
      queue.splice(0, batch.length);
      sentTotal += batch.length;
      lastOk = Date.now();
      lastError = '';
      await saveQueue();
    }
  } catch (e) {
    lastError = String(e && e.message ? e.message : e);
  } finally {
    sending = false;
    updateBadge();
    if (queue.length > 0) {
      // Retry via Alarm (ueberlebt das Einschlafen des Service Workers).
      chrome.alarms.create(RETRY_ALARM, { delayInMinutes: 0.5 });
    }
  }
}

let diag = null;

let checkinCache = { at: 0, list: [] };
async function fetchCheckins() {
  if (Date.now() - checkinCache.at < 15000) return checkinCache.list;
  const cfg = await getConfig();
  if (!cfg.backendUrl || !cfg.apiKey) return [];
  const res = await fetch(cfg.backendUrl.replace(/\/+$/, '') + '/api/checkins', {
    headers: { Authorization: 'Bearer ' + cfg.apiKey },
  });
  if (!res.ok) return [];
  const list = await res.json();
  checkinCache = { at: Date.now(), list: Array.isArray(list) ? list : [] };
  return checkinCache.list;
}

chrome.alarms.onAlarm.addListener((a) => { if (a.name === RETRY_ALARM) flush(); });
chrome.runtime.onStartup.addListener(() => { loadQueue().then(() => { updateBadge(); scheduleFlush(); }); });
chrome.runtime.onInstalled.addListener(() => { loadQueue().then(updateBadge); });

chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (!msg || typeof msg !== 'object') return;
  if (msg.type === 'event' && msg.event) {
    enqueue(msg.event);
    return;
  }
  if (msg.type === 'diag' && msg.diag) {
    diag = msg.diag;
    return;
  }
  if (msg.type === 'status') {
    loadQueue().then(() => sendResponse({ queued: queue.length, lastError, lastOk, sentTotal, diag }));
    return true;
  }
  if (msg.type === 'flush') {
    flush().then(() => sendResponse({ queued: queue ? queue.length : 0, lastError }));
    return true;
  }
  if (msg.type === 'resetDiag') {
    diag = null;
    sentTotal = 0;
    lastError = '';
    sendResponse({ ok: true });
    return true;
  }
  if (msg.type === 'clearQueue') {
    loadQueue().then(async () => { queue = []; await saveQueue(); sendResponse({ ok: true }); });
    return true;
  }
  if (msg.type === 'checkins') {
    fetchCheckins().then((list) => sendResponse({ list })).catch(() => sendResponse({ list: [] }));
    return true;
  }
  if (msg.type === 'test') {
    (async () => {
      try {
        const res = await fetch(msg.backendUrl.replace(/\/+$/, '') + '/api/ingest/ping', {
          headers: { Authorization: 'Bearer ' + msg.apiKey },
        });
        const text = await res.text();
        sendResponse({ ok: res.ok, status: res.status, body: text.slice(0, 300) });
      } catch (e) {
        sendResponse({ ok: false, status: 0, body: String(e && e.message ? e.message : e) });
      }
    })();
    return true;
  }
});
