// Baut den Go-Server, startet ihn mit temporaerer DB auf einem eigenen Port
// (Standard 18200, NIE 8080) und beendet ihn im Teardown.
import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync, createWriteStream } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
const ROOT = resolve(HERE, '../..');
const PORT = Number(process.env.E2E_PORT || 18200);
const ADMIN_PASSWORD = 'e2e-passwort';

async function waitHealth(url, ms) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    try {
      const r = await fetch(url);
      if (r.ok) return;
    } catch {}
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error('Server wurde nicht rechtzeitig bereit: ' + url);
}

export default async function globalSetup() {
  if (PORT === 8080) throw new Error('Port 8080 ist tabu');
  const dir = mkdtempSync(join(tmpdir(), 'yd-tourn-e2e-'));
  const exe = join(dir, process.platform === 'win32' ? 'autodarts-stats.exe' : 'autodarts-stats');

  const b = spawnSync('go', ['build', '-o', exe, './cmd/autodarts-stats'], {
    cwd: join(ROOT, 'server'), stdio: 'inherit', env: { ...process.env, CGO_ENABLED: '0' },
  });
  if (b.status !== 0) throw new Error('go build fehlgeschlagen');

  const env = { ...process.env, DB_PATH: join(dir, 't.db'), PORT: String(PORT), ADMIN_PASSWORD, SESSION_SECRET: 'e2e-secret' };
  const add = spawnSync(exe, ['board', 'add', 'E2E-Board'], { env, encoding: 'utf8' });
  const key = /adb_[A-Za-z0-9_-]+/.exec(add.stdout || '')?.[0];
  if (!key) throw new Error('Board-Key nicht erzeugt: ' + add.stdout + add.stderr);
  process.env.E2E_BOARD_KEY = key;
  process.env.E2E_ADMIN_PASSWORD = ADMIN_PASSWORD;

  const log = createWriteStream(join(dir, 'server.log'));
  const srv = spawn(exe, ['serve'], { env, stdio: ['ignore', 'pipe', 'pipe'] });
  srv.stdout.pipe(log);
  srv.stderr.pipe(log);
  let died = false;
  srv.on('exit', () => (died = true));
  await waitHealth(`http://127.0.0.1:${PORT}/api/health`, 15_000);
  console.log(`Testserver laeuft auf Port ${PORT} (DB in ${dir})`);

  return async () => {
    if (!died) {
      if (process.platform === 'win32') spawnSync('taskkill', ['/PID', String(srv.pid), '/T', '/F']);
      else srv.kill();
    }
    await new Promise((r) => setTimeout(r, 500));
    try { log.end(); rmSync(dir, { recursive: true, force: true }); } catch {}
  };
}
