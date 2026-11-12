import { test, expect, type Page } from '@playwright/test';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import {
  adminLogin, allMatches, beamerUrl, BOARD_KEY, createTournamentApi, enterResultUi, getView, newAdminContext,
  pickWinner, playOutViaUi, readyMatches,
} from './helpers';

// Chromium ohne Extension: der Test beweist, dass Turniere ohne Browser-Extension funktionieren.
// Alle Namen sind eindeutig und kein Name ist Teilstring eines anderen.

function trackExternal(page: Page): string[] {
  const hosts: string[] = [];
  page.on('request', (r) => {
    const u = new URL(r.url());
    if (u.protocol.startsWith('http') && u.hostname !== '127.0.0.1') hosts.push(u.hostname);
  });
  return hosts;
}

test('1 Admin-UI: Spieler, Turnier mit Platz 3 anlegen, starten, alles eintragen, Sieger', async ({ page }) => {
  const external = trackExternal(page);
  const names = ['UiA', 'UiB', 'UiC', 'UiD', 'UiE', 'UiF'];
  await adminLogin(page);

  // Spieler ueber die Admin-Oberflaeche anlegen
  for (const n of names) {
    await page.getByLabel('Neuer Spieler').fill(n);
    await page.locator('form', { hasText: 'Neuer Spieler' }).getByRole('button', { name: 'Anlegen' }).click();
    await expect(page.locator('.player', { hasText: n }).first()).toBeVisible();
  }

  // Turnier ueber die UI anlegen
  await page.goto('/turniere');
  await page.getByRole('button', { name: 'Neues Turnier' }).click();
  await page.getByLabel('Name', { exact: true }).fill('UI-Cup');
  for (const n of names) await page.getByLabel(n, { exact: true }).check();
  await expect(page.getByText('Teilnehmer (6)')).toBeVisible();
  await expect(page.getByLabel('Spiel um Platz 3')).toBeChecked();
  await page.getByRole('button', { name: 'Turnier anlegen' }).click();
  await expect(page).toHaveURL(/\/turniere\/\d+$/);
  const id = Number(page.url().split('/').pop());
  await expect(page.getByRole('heading', { name: 'UI-Cup' })).toBeVisible();

  // Starten
  await page.getByRole('button', { name: /Auslosen/ }).click();
  await expect(page.getByText('Ausgelost, das Turnier läuft.')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Turnierbaum' })).toBeVisible();

  const start = await getView(page.context().request, id);
  expect(start.status).toBe('running');
  expect(start.third, 'Spiel um Platz 3 vorhanden').toBeTruthy();
  expect(start.main.length).toBe(3);
  expect(readyMatches(start).length).toBeGreaterThan(0);

  // Alles durchspielen, jede Runde rueckt nach
  const end = await playOutViaUi(page, id);
  expect(end.champion!.display_name).toBe('UiA');

  await page.goto(`/turniere/${id}`);
  await expect(page.locator('.champion')).toContainText('Turniersieger: UiA');
  await expect(page.locator('tbody tr').first()).toContainText('UiA');
  for (const m of allMatches(end)) expect(['done', 'walkover', 'empty']).toContain(m.status);
  expect(end.standings.find((s) => s.place === 1)!.display_name).toBe('UiA');
  expect(external, 'keine Aufrufe an fremde Hosts (z.B. autodarts.com)').toEqual([]);
});

test('2 Beamer: Reload-Button, Polling, blockierte API behaelt letzten Stand', async ({ browser }) => {
  const admin = await newAdminContext(browser);
  const id = await createTournamentApi(admin.ctx.request, { name: 'Beamer-Cup', names: ['BmA', 'BmB', 'BmC', 'BmD'], rounds: 2 });
  const v0 = await getView(admin.ctx.request, id);
  const [m1, m2] = readyMatches(v0);
  expect(m1 && m2).toBeTruthy();

  // Zweiter Kontext ohne Login
  const viewer = await browser.newContext();
  const beamer = await viewer.newPage();
  await beamer.goto(beamerUrl(id));
  const bracket = beamer.getByTestId('bracket');
  await expect(bracket).toBeVisible();
  await expect(beamer.getByTestId('bracket-match')).not.toHaveCount(0);
  await expect(beamer.getByTestId('bracket-updated')).toBeVisible();
  await expect(beamer.getByTestId('match-winner')).toHaveCount(0);
  await expect(beamer.getByRole('button', { name: 'Eintragen' })).toHaveCount(0);
  const marker = await beamer.evaluate(() => ((window as any).__e2e = Math.random()));

  // Admin traegt Ergebnis 1 ein
  await admin.page.goto(`/turniere/${id}`);
  const w1 = pickWinner(m1);
  await enterResultUi(admin.page, m1, w1.winner);

  // Reload-Button holt den neuen Stand ohne Seitenreload
  const stamp0 = await beamer.getByTestId('bracket-updated').innerText();
  await beamer.waitForTimeout(1100); // Zeitstempel hat evtl. Sekundenaufloesung
  await beamer.getByTestId('bracket-reload').click();
  const m1El = beamer.locator(`[data-testid="bracket-match"][data-key="${m1.key}"]`);
  await expect(m1El.getByTestId('match-winner')).toHaveText(w1.winner);
  await expect(beamer.getByTestId('bracket-updated')).not.toHaveText(stamp0);
  expect(await beamer.evaluate(() => (window as any).__e2e), 'kein Seitenreload').toBe(marker);

  // Zweites Ergebnis kommt automatisch per Polling an (kein Klick)
  const w2 = pickWinner(m2);
  await enterResultUi(admin.page, m2, w2.winner);
  const m2El = beamer.locator(`[data-testid="bracket-match"][data-key="${m2.key}"]`);
  await expect(m2El.getByTestId('match-winner')).toHaveText(w2.winner, { timeout: 60_000 });
  expect(await beamer.evaluate(() => (window as any).__e2e)).toBe(marker);

  // API blockiert: letzter Stand bleibt sichtbar
  await beamer.route('**/api/tournaments/**', (r) => r.abort());
  await beamer.getByTestId('bracket-reload').click();
  await expect(beamer.getByTestId('bracket-offline')).toBeVisible();
  await expect(bracket).toBeVisible();
  await expect(m1El.getByTestId('match-winner')).toHaveText(w1.winner);
  await expect(m2El.getByTestId('match-winner')).toHaveText(w2.winner);

  // Waehrend blockiert spielt der Admin das Finale; Beamer zeigt es erst nach Freigabe
  const mid = await getView(admin.ctx.request, id);
  const fin = readyMatches(mid)[0];
  await enterResultUi(admin.page, fin, pickWinner(fin).winner);
  await beamer.getByTestId('bracket-reload').click();
  await beamer.waitForTimeout(500);
  await expect(beamer.getByTestId('tournament-champion')).toHaveCount(0);
  await expect(m1El.getByTestId('match-winner')).toHaveText(w1.winner);

  await beamer.unroute('**/api/tournaments/**');
  await beamer.getByTestId('bracket-reload').click();
  await expect(beamer.getByTestId('tournament-champion')).toContainText(pickWinner(fin).winner);
  await expect(beamer.getByTestId('bracket-offline')).toHaveCount(0);
  await viewer.close();
  await admin.ctx.close();
});

test('3 Lucky-Loser-Turnier: per API anlegen, per UI durchspielen', async ({ browser }) => {
  const admin = await newAdminContext(browser);
  const names = ['LlA', 'LlB', 'LlC', 'LlD', 'LlE', 'LlF', 'LlG', 'LlH'];
  const id = await createTournamentApi(admin.ctx.request, { name: 'LL-Cup', names, third: true, lucky: true, llEntry: 1 });
  const v = await getView(admin.ctx.request, id);
  expect(v.lucky_rounds.length, 'Lucky-Loser-Runde vorhanden').toBeGreaterThan(0);

  await admin.page.goto(`/turniere/${id}`);
  await expect(admin.page.getByRole('heading', { name: 'Lucky-Loser-Runde' })).toBeVisible();

  const end = await playOutViaUi(admin.page, id);
  await admin.page.goto(`/turniere/${id}`);
  await expect(admin.page.locator('.champion')).toContainText(`Turniersieger: ${end.champion!.display_name}`);
  for (const m of allMatches(end)) expect(['done', 'walkover', 'empty']).toContain(m.status);
  const llPlayed = allMatches(end).filter((m) => (m.bracket === 'll' || m.bracket === 'playin') && m.status === 'done');
  expect(llPlayed.length).toBeGreaterThan(0);
  await admin.ctx.close();
});

test('4 Auto-Zuordnung: ingestiertes Match erscheint im Baum', async ({ browser, request }) => {
  const admin = await newAdminContext(browser);
  const id = await createTournamentApi(admin.ctx.request, { name: 'Auto-Cup', names: ['AuX', 'AuY'], rounds: 1 });
  const v = await getView(admin.ctx.request, id);
  const fin = readyMatches(v)[0];
  expect(fin).toBeTruthy();

  // Fixture aus testdata/placeholder: Spieler 2 gewinnt. Namen und Zeit anpassen.
  const file = resolve(process.cwd(), '../../testdata/placeholder/x01_match_finished.json');
  const body = JSON.parse(readFileSync(file, 'utf8'));
  body.id = 'e2e-auto-0000-0000-000000000001';
  body.createdAt = new Date().toISOString();
  body.players[0].name = 'AuX';
  body.players[1].name = 'AuY'; // Gewinner laut Fixture (winner: 1)

  const res = await request.post('/api/ingest', {
    headers: { Authorization: `Bearer ${BOARD_KEY()}` },
    data: { board: 'E2E', events: [{ kind: 'fetch', url: `https://api.autodarts.com/gs/v0/matches/${body.id}`, ts: Date.now(), body }] },
  });
  expect(res.ok(), await res.text()).toBeTruthy();

  const after = await getView(admin.ctx.request, id);
  expect(after.champion?.display_name).toBe('AuY');
  const done = allMatches(after).find((m) => m.key === fin.key)!;
  expect(done.status).toBe('done');

  await admin.page.goto(`/turniere/${id}`);
  await expect(admin.page.locator('.champion')).toContainText('AuY');
  await admin.page.locator('button.match').first().click();
  await expect(admin.page.locator('.editor')).toContainText('automatisch zugeordnet');

  // Und im Beamer ohne Login
  const viewer = await browser.newContext();
  const beamer = await viewer.newPage();
  await beamer.goto(beamerUrl(id));
  await expect(beamer.getByTestId('bracket-match').first().getByTestId('match-winner')).toHaveText('AuY');
  await viewer.close();
  await admin.ctx.close();
});
