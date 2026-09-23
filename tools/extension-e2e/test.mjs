import { chromium } from 'playwright';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';

// End-to-End-Test der Extension in einem echten Chromium:
// faelscht play.autodarts.com und api.autodarts.com, laedt die Extension und
// prueft, ob das Match im Backend ankommt. Start ueber run.sh bzw. make test-extension.

const PROJECT = process.env.PROJECT_DIR || path.resolve(import.meta.dirname, '../..');
const PORT = process.env.SRV_PORT || '18100';
const KEY = process.env.BOARD_KEY;
const BACKEND = `http://localhost:${PORT}`;

// Kopie der Extension mit Host-Permission fuer den Test-Backend-Port,
// damit im Test kein interaktiver Berechtigungsdialog noetig ist.
const extDir = path.join(os.tmpdir(), 'ydext-' + Date.now());
fs.cpSync(path.join(PROJECT, 'extension'), extDir, { recursive: true });
const mf = JSON.parse(fs.readFileSync(path.join(extDir, 'manifest.json'), 'utf8'));
mf.host_permissions = ['https://play.autodarts.com/*', 'https://play.autodarts.io/*', `http://localhost:${PORT}/*`];
fs.writeFileSync(path.join(extDir, 'manifest.json'), JSON.stringify(mf, null, 2));

const matchJson = fs.readFileSync(path.join(PROJECT, 'testdata/placeholder/x01_match_finished.json'), 'utf8');

const PAGE = `<!doctype html><html><head><meta charset="utf-8"><title>Fake Autodarts</title></head>
<body><h1>Fake Autodarts</h1><div id="out">start</div>
<script>
(async () => {
  const log = (m) => { document.getElementById('out').textContent += ' | ' + m; };
  try {
    const r = await fetch('https://api.autodarts.com/gs/v0/matches/11111111-2222-3333-4444-555555555555');
    const j = await r.json();
    log('fetch ok ' + j.id);
  } catch (e) { log('fetch fail ' + e.message); }
  try {
    const x = new XMLHttpRequest();
    x.open('GET', 'https://api.autodarts.com/as/v0/matches/xhr-variante/stats');
    x.onload = () => log('xhr ' + x.status);
    x.send();
  } catch (e) { log('xhr fail ' + e.message); }
  log('hook=' + (window.__adstatsHooked === true));
})();
</script></body></html>`;

const userDataDir = path.join(os.tmpdir(), 'ydprofile-' + Date.now());
const ctx = await chromium.launchPersistentContext(userDataDir, {
  channel: 'chromium',
  headless: true,
  args: [`--disable-extensions-except=${extDir}`, `--load-extension=${extDir}`],
});

// Service Worker der Extension abwarten
let sw = ctx.serviceWorkers()[0];
if (!sw) sw = await ctx.waitForEvent('serviceworker', { timeout: 20000 });
const extId = new URL(sw.url()).host;
console.log('Extension-ID:', extId);
sw.on('console', (m) => console.log('  [SW ' + m.type() + ']', m.text()));

// Konfiguration in die Extension schreiben
await sw.evaluate(async ([backendUrl, apiKey]) => {
  await chrome.storage.sync.set({ backendUrl, apiKey, boardName: 'Testboard', captureAll: false, showOverlay: true });
}, [BACKEND, KEY]);
console.log('Konfiguration gesetzt');

// Netzwerk faelschen: play.autodarts.com und api.autodarts.com
await ctx.route('https://play.autodarts.{com,io}/**', (route) =>
  route.fulfill({ status: 200, contentType: 'text/html; charset=utf-8', body: PAGE }));
await ctx.route('https://api.autodarts.com/**', (route) =>
  route.fulfill({ status: 200, contentType: 'application/json', body: matchJson }));

const page = await ctx.newPage();
const consoleErrors = [];
page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(m.text()); });
await page.goto('https://play.autodarts.com/', { waitUntil: 'load' });
await page.waitForTimeout(1500);
const out = await page.locator('#out').textContent();
console.log('Testseite:', out.trim());

// Warten, bis die Extension gesendet hat
await page.waitForTimeout(3500);

// Diagnose in der Options-Seite ablesen
const opt = await ctx.newPage();
await opt.goto(`chrome-extension://${extId}/options.html`);
await opt.waitForTimeout(2500);
const diagText = (await opt.locator('#diagBox').innerText()).replace(/\n+/g, ' | ');
console.log('Diagnose:', diagText);
console.log('Status:', (await opt.locator('#status').innerText()).replace(/\n+/g, ' | ') || '(leer)');

// Flush erzwingen und Fehler direkt abfragen
const forced = await opt.evaluate(() => new Promise((res) => {
  chrome.runtime.sendMessage({ type: 'flush' }, (r) => res(r || { err: String(chrome.runtime.lastError) }));
}));
console.log('Erzwungener Flush:', JSON.stringify(forced));

// Direkter fetch-Versuch aus dem Extension-Kontext
const direct = await opt.evaluate(async (backend) => {
  try {
    const r = await fetch(backend + '/api/health');
    return 'health ' + r.status;
  } catch (e) { return 'FEHLER ' + e.message; }
}, BACKEND);
console.log('Direkter fetch aus Extension:', direct);
await opt.waitForTimeout(1500);

// Ergebnis im Backend pruefen
const res = await fetch(`${BACKEND}/api/matches?limit=5`);
const matches = await res.json();
console.log('Matches im Backend:', matches.length);
if (matches.length) {
  console.log('  Spieler:', matches[0].players.map((p) => `${p.display_name}${p.won ? ' (Sieger)' : ''}`).join(', '));
  console.log('  Legs:', matches[0].players.map((p) => p.legs_won).join(':'));
}
if (consoleErrors.length) console.log('Konsolenfehler:', consoleErrors.slice(0, 3));

await ctx.close();
fs.rmSync(extDir, { recursive: true, force: true });
fs.rmSync(userDataDir, { recursive: true, force: true });
console.log(matches.length > 0 ? 'ERGEBNIS: OK' : 'ERGEBNIS: NICHTS ANGEKOMMEN');
process.exit(matches.length > 0 ? 0 : 1);
