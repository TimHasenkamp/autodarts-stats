import { test, expect } from '@playwright/test';
import {
  allMatches, beamerUrl, confirmOk, createPlayerApi, createTournamentApi, enterResultUi, getView, matchEl,
  newAdminContext, pickWinner, pidOf, playOutViaUi, readyMatches, replaceUi, rowOf, withdrawUi, type Match,
} from './helpers';

// Teilnehmer im laufenden Turnier aendern. Spielernamen sind eindeutig, keiner ist Teilstring eines anderen.
// Auslosung ist zufaellig: alle Erwartungen werden aus der Ansicht (API) abgeleitet.

const sideName = (m: Match, won: boolean) => (m.a.won === won ? m.a : m.b).display_name;
const isBye = (m: Match) => m.status === 'walkover' && (m.b as any).placeholder === 'Freilos';

test('5 Ersetzen nach Runde 1: vorhandener und neuer Name, altes Ergebnis bleibt, Turnier bis Sieger', async ({ browser }) => {
  const { ctx, page } = await newAdminContext(browser);
  const names = ['RpA', 'RpB', 'RpC', 'RpD', 'RpE', 'RpF', 'RpG', 'RpH'];
  const id = await createTournamentApi(ctx.request, { name: 'Ersatz-Cup', names, rounds: 3 });
  await createPlayerApi(ctx.request, 'RpExist');
  await page.goto(`/turniere/${id}`);

  // Runde 1 komplett (4 Spiele)
  for (let i = 0; i < 4; i++) {
    const v = await getView(ctx.request, id);
    const m = readyMatches(v).find((x) => v.main[0].matches.some((r) => r.key === x.key))!;
    await enterResultUi(page, m, pickWinner(m).winner);
  }
  let v = await getView(ctx.request, id);
  expect(v.main[0].matches.every((m) => m.status === 'done')).toBeTruthy();
  const r1 = v.main[0].matches.map((m) => ({ key: m.key, winner: sideName(m, true)!, loser: sideName(m, false)! }));
  const [semi] = readyMatches(v);
  const oldA = semi.a.display_name!, oldB = semi.b.display_name!;

  await page.goto(`/turniere/${id}`);
  await replaceUi(page, pidOf(v, oldA), { existing: 'RpExist' });
  await expect(page.getByText(`RpExist ersetzt ${oldA}.`)).toBeVisible();
  await replaceUi(page, pidOf(v, oldB), { newName: 'RpNeu' });

  v = await getView(ctx.request, id);
  const after = allMatches(v).find((m) => m.key === semi.key)!;
  expect([after.a.display_name, after.b.display_name].sort()).toEqual(['RpExist', 'RpNeu']);
  // Fruehere Ergebnisse stehen weiter beim Ersetzten
  for (const r of r1) {
    const m = allMatches(v).find((x) => x.key === r.key)!;
    expect(sideName(m, true)).toBe(r.winner);
    expect(sideName(m, false)).toBe(r.loser);
    await expect(matchEl(page, r.key).getByTestId('match-winner')).toHaveText(r.winner);
  }
  const rowOld = rowOf(page, pidOf(v, oldA));
  await expect(rowOld).toContainText('RpExist');
  await expect(rowOld).toHaveClass(/struck/);
  await expect(rowOld.getByTestId('participant-replace')).toHaveCount(0);
  await expect(rowOf(page, pidOf(v, 'RpNeu'))).toBeVisible();

  const end = await playOutViaUi(page, id);
  expect([oldA, oldB]).not.toContain(end.champion!.display_name);
  // Ersatz hat im Halbfinale tatsaechlich gespielt
  const semiDone = allMatches(end).find((m) => m.key === semi.key)!;
  expect(semiDone.status).toBe('done');
  expect(['RpExist', 'RpNeu']).toContain(sideName(semiDone, true));
  await ctx.close();
});

test('6 Zurueckziehen (Aufgabe): Gegner kampflos weiter, w.o. auch im Beamer, dann Rueckgaengig', async ({ browser }) => {
  const { ctx, page } = await newAdminContext(browser);
  const id = await createTournamentApi(ctx.request, { name: 'Rueckzug-Cup', names: ['WdA', 'WdB', 'WdC', 'WdD'], rounds: 2 });
  let v = await getView(ctx.request, id);
  const semi = readyMatches(v)[0];
  const quitter = semi.a.display_name!, opp = semi.b.display_name!;
  const pid = pidOf(v, quitter);

  await page.goto(`/turniere/${id}`);
  await withdrawUi(page, pid, 'retired');
  await expect(page.getByText(`${quitter} zurückgezogen.`)).toBeVisible();

  const card = matchEl(page, semi.key);
  await expect(card.getByTestId('match-outcome')).toHaveAttribute('data-outcome', 'withdrawn');
  await expect(card.getByTestId('match-outcome')).toHaveText('w.o.');
  await expect(card.locator('[data-withdrawn="true"]')).toContainText(quitter);
  await expect(card.getByTestId('match-winner')).toHaveText(opp);
  await expect(rowOf(page, pid)).toHaveClass(/struck/);
  await expect(rowOf(page, pid).getByTestId('participant-undo-withdraw')).toBeVisible();

  v = await getView(ctx.request, id);
  const fin = v.main[1].matches[0];
  expect([fin.a.display_name, fin.b.display_name]).toContain(opp);

  // Beamer ohne Login, nach Reload
  const viewer = await browser.newContext();
  const beamer = await viewer.newPage();
  await beamer.goto(beamerUrl(id));
  const bm = matchEl(beamer, semi.key);
  await expect(bm.getByTestId('match-outcome')).toHaveText('w.o.');
  await expect(bm.locator('[data-withdrawn="true"]')).toContainText(quitter);
  await beamer.reload();
  await expect(matchEl(beamer, semi.key).getByTestId('match-outcome')).toHaveAttribute('data-outcome', 'withdrawn');
  await expect(matchEl(beamer, semi.key).locator('[data-withdrawn="true"]')).toContainText(quitter);
  await expect(matchEl(beamer, semi.key).getByTestId('match-winner')).toHaveText(opp);

  // Rueckgaengig
  await rowOf(page, pid).getByTestId('participant-undo-withdraw').click();
  await confirmOk(page);
  await expect(page.getByText(`${quitter} spielt wieder mit.`)).toBeVisible();
  await expect(card.getByTestId('match-outcome')).toHaveCount(0);
  await expect(card).toHaveAttribute('data-status', 'ready');
  await expect(card.locator('[data-withdrawn="true"]')).toHaveCount(0);
  await expect(rowOf(page, pid).getByTestId('participant-withdraw')).toBeVisible();
  await beamer.reload();
  await expect(matchEl(beamer, semi.key).getByTestId('match-outcome')).toHaveCount(0);
  await viewer.close();
  await ctx.close();
});

test('7 Einzelergebnis Aufgabe ueber Ausgang-Auswahl', async ({ browser }) => {
  const { ctx, page } = await newAdminContext(browser);
  const id = await createTournamentApi(ctx.request, { name: 'Aufgabe-Cup', names: ['RtA', 'RtB', 'RtC', 'RtD'], rounds: 2 });
  const m = readyMatches(await getView(ctx.request, id))[0];
  const { winner, loser } = pickWinner(m);

  await page.goto(`/turniere/${id}`);
  await matchEl(page, m.key).click();
  const editor = page.locator('.editor');
  await editor.getByLabel('Sieger').selectOption({ label: winner });
  await editor.getByTestId('result-outcome').selectOption('retired');
  await editor.getByLabel(`Legs ${loser}`).fill('0');
  await editor.getByTestId('result-save').click();
  await expect(page.getByText('Aufgabe/Disqualifikation eingetragen.')).toBeVisible();

  const card = matchEl(page, m.key);
  await expect(card.getByTestId('match-outcome')).toHaveAttribute('data-outcome', 'retired');
  await expect(card.getByTestId('match-outcome')).toHaveText('Aufgabe');
  await expect(card.getByTestId('match-winner')).toHaveText(winner);
  const v = await getView(ctx.request, id);
  const done = allMatches(v).find((x) => x.key === m.key)!;
  expect(done.status).toBe('done');
  expect((done as any).outcome).toBe('retired');
  const fin = v.main[1].matches[0];
  expect([fin.a.display_name, fin.b.display_name]).toContain(winner);
  await ctx.close();
});

test('8 Nachmelden auf Freilos: erlaubt, danach nach gespielter Folgerunde abgelehnt', async ({ browser }) => {
  const { ctx, page } = await newAdminContext(browser);
  const six = (p: string) => ['A', 'B', 'C', 'D', 'E', 'F'].map((x) => p + x);

  // (1) erlaubt: noch nichts gespielt
  const id1 = await createTournamentApi(ctx.request, { name: 'Late-Cup-1', names: six('Lt'), rounds: 3 });
  let v = await getView(ctx.request, id1);
  const bye = v.main[0].matches.find(isBye)!;
  expect(bye, 'Freilos in Runde 1').toBeTruthy();
  const holder = bye.a.display_name!;
  await page.goto(`/turniere/${id1}`);
  await page.locator(`[data-testid="late-entry"][data-key="${bye.key}"]`).click();
  const panel = page.getByTestId('participant-panel');
  await expect(panel).toHaveAttribute('data-kind', 'late');
  await panel.getByTestId('newcomer-name').fill('LtNeu');
  await panel.getByTestId('participant-panel-submit').click();
  await confirmOk(page);
  await expect(page.getByText('LtNeu nachgemeldet.')).toBeVisible();
  await expect(matchEl(page, bye.key)).toHaveAttribute('data-status', 'ready');
  await expect(matchEl(page, bye.key)).toContainText(holder);
  await expect(matchEl(page, bye.key)).toContainText('LtNeu');
  await expect(page.locator(`[data-testid="late-entry"][data-key="${bye.key}"]`)).toHaveCount(0);
  await expect(rowOf(page, pidOf(await getView(ctx.request, id1), 'LtNeu'))).toBeVisible();

  // (2) abgelehnt: Folgerunde mit dem Freilos-Inhaber bereits gespielt
  const id2 = await createTournamentApi(ctx.request, { name: 'Late-Cup-2', names: six('Lu'), rounds: 3 });
  v = await getView(ctx.request, id2);
  const bye2 = v.main[0].matches.find(isBye)!;
  const holder2 = bye2.a.display_name!;
  const plays = (m: Match) => m.a.display_name === holder2 || m.b.display_name === holder2;
  await page.goto(`/turniere/${id2}`);
  for (let guard = 0; guard < 6; guard++) {
    v = await getView(ctx.request, id2);
    if (allMatches(v).some((m) => m.status === 'done' && plays(m))) break;
    const m = readyMatches(v).find(plays) ?? readyMatches(v)[0];
    await enterResultUi(page, m, pickWinner(m).winner);
  }
  v = await getView(ctx.request, id2);
  expect(allMatches(v).some((m) => m.status === 'done' && plays(m))).toBeTruthy();

  await page.locator(`[data-testid="late-entry"][data-key="${bye2.key}"]`).click();
  await page.getByTestId('newcomer-name').fill('LuNeu');
  await page.getByTestId('participant-panel-submit').click();
  await confirmOk(page);
  await expect(page.locator('p.error')).toContainText('Nachmelden nicht mehr moeglich');
  const v2 = await getView(ctx.request, id2);
  expect(v2.standings.map((s) => s.display_name)).not.toContain('LuNeu');
  expect(allMatches(v2).find((m) => m.key === bye2.key)!.status).toBe('walkover');
  await ctx.close();
});

test('9 Tauschen zweier Spieler vor ihren Spielen', async ({ browser }) => {
  const { ctx, page } = await newAdminContext(browser);
  const id = await createTournamentApi(ctx.request, { name: 'Tausch-Cup', names: ['SwA', 'SwB', 'SwC', 'SwD'], rounds: 2 });
  let v = await getView(ctx.request, id);
  const [m1, m2] = readyMatches(v);
  const x = m1.a.display_name!, y = m2.a.display_name!;

  await page.goto(`/turniere/${id}`);
  await rowOf(page, pidOf(v, x)).getByTestId('participant-swap').click();
  const panel = page.getByTestId('participant-panel');
  await expect(panel).toHaveAttribute('data-kind', 'swap');
  await panel.getByTestId('swap-select').selectOption({ label: y });
  await panel.getByTestId('participant-panel-submit').click();
  await confirmOk(page);
  await expect(page.getByText('Plätze getauscht.')).toBeVisible();

  v = await getView(ctx.request, id);
  const n1 = allMatches(v).find((m) => m.key === m1.key)!, n2 = allMatches(v).find((m) => m.key === m2.key)!;
  expect(n1.a.display_name).toBe(y);
  expect(n1.b.display_name).toBe(m1.b.display_name);
  expect(n2.a.display_name).toBe(x);
  expect(n2.b.display_name).toBe(m2.b.display_name);
  await expect(matchEl(page, m1.key)).toContainText(y);
  await expect(matchEl(page, m2.key)).toContainText(x);

  // Nach einem gespielten Ergebnis ist Tauschen fuer den Spieler nicht mehr angeboten
  await enterResultUi(page, n1, pickWinner(n1).winner);
  await expect(rowOf(page, pidOf(v, y)).getByTestId('participant-swap')).toHaveCount(0);
  await ctx.close();
});

test('10 Korrektur in Runde 1: confirm-dialog mit betroffenen Paarungen, Abbrechen aendert nichts, OK erzwingt', async ({ browser }) => {
  const { ctx, page } = await newAdminContext(browser);
  const id = await createTournamentApi(ctx.request, { name: 'Korrektur-Cup', names: ['KoA', 'KoB', 'KoC', 'KoD'], rounds: 2 });
  await page.goto(`/turniere/${id}`);
  const semi1 = readyMatches(await getView(ctx.request, id))[0];
  await enterResultUi(page, semi1, pickWinner(semi1).winner);
  const semi2 = readyMatches(await getView(ctx.request, id))[0];
  await enterResultUi(page, semi2, pickWinner(semi2).winner);
  const fin = readyMatches(await getView(ctx.request, id))[0];
  await enterResultUi(page, fin, pickWinner(fin).winner);

  const before = await getView(ctx.request, id);
  expect(before.champion).toBeTruthy();
  const { winner, loser } = pickWinner(semi1);

  const correct = async () => {
    await matchEl(page, semi1.key).click();
    const editor = page.locator('.editor');
    await editor.getByLabel('Sieger').selectOption({ label: loser });
    await editor.getByLabel(`Legs ${winner}`).fill('1');
    await editor.getByTestId('result-save').click();
  };

  // Abbrechen
  await correct();
  const dlg = page.getByTestId('confirm-dialog');
  await expect(dlg).toBeVisible();
  const list = page.getByTestId('confirm-dialog-list');
  await expect(list).toContainText(/Finale|Spiel \d+/);
  expect(await list.locator('li').count()).toBeGreaterThanOrEqual(1);
  await page.getByTestId('confirm-cancel').click();
  await expect(dlg).toHaveCount(0);
  const unchanged = await getView(ctx.request, id);
  expect(unchanged.champion?.display_name).toBe(before.champion!.display_name);
  const u1 = allMatches(unchanged).find((m) => m.key === semi1.key)!;
  expect(u1.status).toBe('done');
  expect(sideName(u1, true)).toBe(winner);
  expect(allMatches(unchanged).find((m) => m.key === fin.key)!.status).toBe('done');
  await expect(matchEl(page, semi1.key).getByTestId('match-winner')).toHaveText(winner);

  // OK erzwingt
  await correct();
  await expect(dlg).toBeVisible();
  await confirmOk(page);
  await expect(page.getByText('Ergebnis eingetragen.')).toBeVisible();
  const forced = await getView(ctx.request, id);
  const s1 = allMatches(forced).find((m) => m.key === semi1.key)!;
  expect(sideName(s1, true)).toBe(loser);
  expect(forced.champion ?? null).toBeNull();
  expect(allMatches(forced).find((m) => m.key === fin.key)!.status).not.toBe('done');
  await expect(matchEl(page, semi1.key).getByTestId('match-winner')).toHaveText(loser);
  await expect(page.getByTestId('tournament-champion')).toHaveCount(0);
  await ctx.close();
});

test('11 Admin: Spieler im laufenden Turnier loeschen wird abgelehnt (dort ersetzen)', async ({ browser }) => {
  const { ctx, page } = await newAdminContext(browser);
  await createTournamentApi(ctx.request, { name: 'Loesch-Cup', names: ['DlA', 'DlB'], rounds: 1 });
  await page.goto('/admin');
  const card = page.locator('.player', { hasText: 'DlA' }).first();
  await expect(card).toBeVisible();
  page.once('dialog', (d) => d.accept());
  await card.getByRole('button', { name: 'Löschen' }).click();
  await expect(page.locator('p.error')).toContainText('dort ersetzen');
  await expect(page.locator('p.error')).toContainText('Loesch-Cup');
  await expect(page.locator('.player', { hasText: 'DlA' }).first()).toBeVisible();
  await ctx.close();
});
