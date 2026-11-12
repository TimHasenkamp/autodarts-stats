import { expect, type APIRequestContext, type Browser, type BrowserContext, type Page } from '@playwright/test';

export const ADMIN_PASSWORD = () => process.env.E2E_ADMIN_PASSWORD!;
export const BOARD_KEY = () => process.env.E2E_BOARD_KEY!;

export interface Side { player_id?: number; display_name?: string; legs?: number; won: boolean }
export interface Match { key: string; no: number; status: string; bracket: string; a: Side; b: Side; rule: { first_to: number } }
export interface View {
  id: number; name: string; status: string; champion?: { display_name: string; player_id: number } | null;
  main: { name: string; matches: Match[] }[]; lucky_rounds: { name: string; matches: Match[] }[];
  playin?: Match; third?: Match; next: Match[];
  standings: { place: number; display_name: string }[];
}

export function allMatches(v: View): Match[] {
  const out = [...v.main, ...v.lucky_rounds].flatMap((r) => r.matches);
  if (v.playin) out.push(v.playin);
  if (v.third) out.push(v.third);
  return out;
}

export async function getView(req: APIRequestContext, id: number): Promise<View> {
  const r = await req.get(`/api/tournaments/${id}`);
  expect(r.ok()).toBeTruthy();
  return r.json();
}

/** Meldet einen Kontext als Admin an (Cookie landet im Kontext, auch fuer context.request). */
export async function adminLogin(page: Page) {
  await page.goto('/admin');
  await page.getByLabel('Passwort').fill(ADMIN_PASSWORD());
  await page.getByRole('button', { name: 'Anmelden' }).click();
  await expect(page.getByRole('button', { name: 'Abmelden' })).toBeVisible();
}

export async function newAdminContext(browser: Browser): Promise<{ ctx: BrowserContext; page: Page }> {
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  await adminLogin(page);
  return { ctx, page };
}

/** Legt ein Turnier ueber die Admin-API an und startet es (schnell, fuer Szenarien ohne UI-Anlage). */
export async function createTournamentApi(
  req: APIRequestContext,
  o: { name: string; names: string[]; third?: boolean; lucky?: boolean; llEntry?: number; rounds?: number; start?: boolean },
): Promise<number> {
  const rule = { variant: 'X01', base_score: 501, first_to: 2 };
  const r = await req.post('/api/admin/tournaments', {
    data: {
      name: o.name, player_ids: [], new_names: o.names,
      third_place: !!o.third, lucky_loser: !!o.lucky, ll_entry: o.llEntry ?? 1,
      rules: { rounds: Array.from({ length: o.rounds ?? 3 }, () => ({ ...rule })), lucky_loser: rule },
    },
  });
  expect(r.ok(), await r.text()).toBeTruthy();
  const { id } = await r.json();
  if (o.start !== false) {
    const s = await req.post(`/api/admin/tournaments/${id}/start`);
    expect(s.ok(), await s.text()).toBeTruthy();
  }
  return id;
}

/** Offene Spiele (beide Seiten bekannt). */
export function readyMatches(v: View): Match[] {
  return allMatches(v).filter((m) => m.status === 'ready' && m.a.display_name && m.b.display_name);
}

/** Der alphabetisch kleinere Name gewinnt immer: deterministischer Sieger. */
export function pickWinner(m: Match): { winner: string; loser: string } {
  const [w, l] = [m.a.display_name!, m.b.display_name!].sort();
  return { winner: w, loser: l };
}

/**
 * Traegt ein Ergebnis ueber die Admin-Oberflaeche ein (Seite /turniere/{id}):
 * Spiel im Baum anklicken, Sieger waehlen, Legs des Verlierers setzen, Eintragen.
 */
export async function enterResultUi(page: Page, m: Match, winner: string, loserLegs = 1) {
  const a = m.a.display_name!, b = m.b.display_name!;
  const loser = winner === a ? b : a;
  const card = page.locator(`button.match:not([disabled])[data-key="${m.key}"]`);
  await expect(card).toHaveCount(1);
  await card.click();
  const editor = page.locator('.editor');
  await expect(editor).toContainText(`Spiel ${m.no}: ${a} vs ${b}`);
  await editor.getByLabel('Sieger').selectOption({ label: winner });
  await editor.getByLabel(`Legs ${loser}`).fill(String(loserLegs));
  await editor.getByRole('button', { name: 'Eintragen' }).click();
  await expect(page.getByText('Ergebnis eingetragen.')).toBeVisible();
  await expect(editor).toHaveCount(0);
}

/** Spielt ein Turnier ueber die UI bis zum Sieger durch und prueft dabei das Nachruecken. */
export async function playOutViaUi(page: Page, id: number): Promise<View> {
  let v = await getView(page.context().request, id);
  // Weiterspielen, bis nichts mehr offen ist (der Sieger steht evtl. schon vor dem Spiel um Platz 3 fest).
  for (let guard = 0; guard < 40; guard++) {
    const ready = readyMatches(v);
    if (ready.length === 0) break;
    expect(guard, 'Turnier kommt nicht zum Ende').toBeLessThan(39);
    const m = ready[0];
    const { winner } = pickWinner(m);
    await page.goto(`/turniere/${id}`);
    await enterResultUi(page, m, winner);
    const after = await getView(page.context().request, id);
    const done = allMatches(after).find((x) => x.key === m.key)!;
    expect(done.status, `Spiel ${m.key} muss erledigt sein`).toBe('done');
    const w = done.a.won ? done.a : done.b;
    expect(w.display_name).toBe(winner);
    // Der Sieger muss in einem anderen (spaeteren) Spiel auftauchen, ausser im Finale/Platz 3.
    const elsewhere = allMatches(after).some((x) => x.key !== m.key && (x.a.display_name === winner || x.b.display_name === winner));
    const finalLike = m.bracket === 'third' || after.main.at(-1)!.matches.some((x) => x.key === m.key);
    if (!finalLike) expect(elsewhere, `${winner} muss nachruecken`).toBeTruthy();
    v = after;
  }
  expect(v.champion, 'Turniersieger muss feststehen').toBeTruthy();
  expect(readyMatches(v), 'kein offenes Spiel mehr').toHaveLength(0);
  return v;
}

// ---- Teilnehmer im laufenden Turnier ----

export const rowOf = (page: Page, pid: number) => page.locator(`[data-testid="participant-row"][data-player-id="${pid}"]`);
export const matchEl = (page: Page, key: string) => page.locator(`[data-testid="bracket-match"][data-key="${key}"]`);

export function pidOf(v: View, name: string): number {
  const p = v.standings.find((s) => s.display_name === name) as any;
  expect(p, `Spieler ${name} in Platzierungen`).toBeTruthy();
  return p.player_id;
}

export async function createPlayerApi(req: APIRequestContext, name: string) {
  const r = await req.post('/api/admin/players', { data: { display_name: name } });
  expect(r.ok(), await r.text()).toBeTruthy();
}

export async function confirmOk(page: Page) {
  const d = page.getByTestId('confirm-dialog');
  await expect(d).toBeVisible();
  await page.getByTestId('confirm-ok').click();
  await expect(d).toHaveCount(0);
}

/** Ersetzen ueber die Teilnehmertabelle. Entweder vorhandener Spieler (existing) oder neuer Name. */
export async function replaceUi(page: Page, pid: number, to: { existing?: string; newName?: string }) {
  await rowOf(page, pid).getByTestId('participant-replace').click();
  const panel = page.getByTestId('participant-panel');
  await expect(panel).toHaveAttribute('data-kind', 'replace');
  if (to.existing) await panel.getByTestId('newcomer-select').selectOption({ label: to.existing });
  else await panel.getByTestId('newcomer-name').fill(to.newName!);
  await panel.getByTestId('participant-panel-submit').click();
  await confirmOk(page);
  await expect(panel).toHaveCount(0);
}

/** Zurueckziehen / Disqualifizieren ueber die Teilnehmertabelle. */
export async function withdrawUi(page: Page, pid: number, reason: 'retired' | 'dq') {
  await rowOf(page, pid).getByTestId('participant-withdraw').click();
  const panel = page.getByTestId('participant-panel');
  await expect(panel).toHaveAttribute('data-kind', 'withdraw');
  await panel.getByTestId('withdraw-reason').selectOption(reason);
  await panel.getByTestId('participant-panel-submit').click();
  await confirmOk(page);
  await expect(panel).toHaveCount(0);
}

/** Beamer-Baumansicht.Eigene Baum-Route mit kurzem Polling-Intervall (?intervall=5, Minimum 5 s). */
export const beamerUrl = (id: number) => `/turniere/${id}/beamer/baum?intervall=5`;
