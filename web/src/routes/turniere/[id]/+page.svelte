<script lang="ts">
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { api, ApiError, type Candidate, type TConflict, type TMatch, type TParticipant, type TRound, type TournamentInput, type TournamentView } from '$lib/api';
  import Bracket from '$lib/components/Bracket.svelte';
  import TournamentForm from '$lib/components/TournamentForm.svelte';
  import { dateTime, num } from '$lib/format';
  import { ruleText, statusText } from '$lib/tournament';

  let view: TournamentView | null = $state(null);
  let error = $state('');
  let info = $state('');
  let admin = $state(false);
  let editing = $state(false);
  let selectedKey = $state('');
  let candidates: Candidate[] = $state([]);
  let manual = $state({ winner: 0, legs1: 0, legs2: 0, outcome: '' as '' | 'retired' | 'dq' });

  // Bestätigungsdialog (auch für 409: betroffene Paarungen).
  type Dialog = { title: string; text: string; list?: string[]; okLabel?: string; danger?: boolean; ok?: () => Promise<void> };
  let dialog: Dialog | null = $state(null);
  let dialogBusy = $state(false);

  // Teilnehmer-Aktion (Ersetzen, Tauschen, Zurückziehen, Nachmelden) mit Eingaben.
  type PAction =
    | { kind: 'replace'; p: TParticipant }
    | { kind: 'swap'; p: TParticipant }
    | { kind: 'withdraw'; p: TParticipant }
    | { kind: 'late'; m: TMatch };
  let paction: PAction | null = $state(null);
  let pform = $state({ playerId: 0, newName: '', swapWith: 0, reason: 'retired' as 'retired' | 'dq' });
  let allPlayers: { id: number; display_name: string }[] = $state([]);

  const id = $derived(page.params.id);
  const selected = $derived(view ? findMatch(view, selectedKey) : null);
  // Lucky-Loser-Runden plus Zusatzspiel als letzte Spalte.
  const llRounds = $derived.by((): TRound[] => {
    if (!view) return [];
    const rs = [...view.lucky_rounds];
    if (view.playin) rs.push({ name: 'Zusatzspiel', rule: view.playin.rule, matches: [view.playin] });
    return rs;
  });

  function findMatch(v: TournamentView, key: string): TMatch | null {
    if (!key) return null;
    const all = [...v.main, ...v.lucky_rounds].flatMap((r) => r.matches);
    if (v.playin) all.push(v.playin);
    if (v.third) all.push(v.third);
    return all.find((m) => m.key === key) ?? null;
  }

  async function load() {
    try {
      view = await api.get<TournamentView>(`/api/tournaments/${id}`);
      error = '';
    } catch (e) {
      error = (e as Error).message;
    }
  }

  $effect(() => {
    void id;
    load();
  });
  // Live-Aktualisierung, solange das Turnier läuft.
  $effect(() => {
    if (view?.status !== 'running' || editing) return;
    const t = setInterval(load, 15000);
    return () => clearInterval(t);
  });
  api.get<{ admin: boolean }>('/api/admin/me').then((m) => (admin = m.admin)).catch(() => {});

  async function act(fn: () => Promise<unknown>, msg = '') {
    error = '';
    info = '';
    try {
      await fn();
      info = msg;
      await load();
    } catch (e) {
      error = (e as Error).message;
    }
  }

  function conflictOf(e: unknown): TConflict | null {
    if (e instanceof ApiError && e.status === 409 && e.data && typeof e.data === 'object' && 'affected' in e.data) return e.data as TConflict;
    return null;
  }

  /** Führt eine Änderung aus; verfallen dadurch spätere Ergebnisse (409), wird erst nach Bestätigung mit force=1 wiederholt. */
  async function actForce(fn: (force: boolean) => Promise<unknown>, msg: string) {
    error = '';
    info = '';
    try {
      await fn(false);
      info = msg;
      await load();
    } catch (e) {
      const c = conflictOf(e);
      if (!c) {
        error = (e as Error).message;
        return;
      }
      dialog = {
        title: c.forceable ? 'Spätere Ergebnisse verfallen' : 'Nicht möglich',
        text: c.forceable ? 'Durch diese Änderung verfallen die Ergebnisse dieser Paarungen:' : c.error,
        list: c.affected.map((a) => a.label),
        okLabel: c.forceable ? 'Trotzdem ändern' : undefined,
        danger: true,
        ok: c.forceable ? () => act(() => fn(true), msg) : undefined,
      };
    }
  }

  function ask(d: Dialog) {
    dialog = d;
  }
  async function dialogOk() {
    if (!dialog?.ok) return;
    const run = dialog.ok;
    dialogBusy = true;
    dialog = null;
    try {
      await run();
    } finally {
      dialogBusy = false;
    }
  }

  const fq = (force: boolean) => (force ? '?force=1' : '');

  const base = $derived(`/api/admin/tournaments/${id}`);

  async function select(m: TMatch) {
    if (!admin) return;
    if (selectedKey === m.key) {
      selectedKey = '';
      return;
    }
    selectedKey = m.key;
    manual = { winner: 0, legs1: m.rule.first_to, legs2: 0, outcome: '' };
    candidates = [];
    try {
      candidates = await api.get<Candidate[]>(`${base}/candidates/${m.key}`);
    } catch (e) {
      error = (e as Error).message;
    }
  }

  function setWinner(pid: number, m: TMatch) {
    manual.winner = pid;
    const ft = m.rule.first_to;
    if (pid === m.a.player_id) { manual.legs1 = ft; if (manual.legs2 >= ft) manual.legs2 = 0; }
    else { manual.legs2 = ft; if (manual.legs1 >= ft) manual.legs1 = 0; }
  }

  async function save(input: TournamentInput) {
    await act(() => api.put(base, input), 'Gespeichert.');
    if (!error) editing = false;
  }
  async function start() {
    await act(() => api.post(`${base}/start`), 'Ausgelost, das Turnier läuft.');
  }
  async function redraw() {
    if (!confirm('Neu auslosen? Der aktuelle Turnierbaum wird verworfen.')) return;
    await act(() => api.post(`${base}/redraw`), 'Neu ausgelost.');
  }
  async function remove() {
    if (!view || !confirm(`Turnier „${view.name}“ mit allen Ergebnissen löschen? Die Matches selbst bleiben erhalten.`)) return;
    try {
      await api.del(base);
      await goto('/turniere');
    } catch (e) {
      error = (e as Error).message;
    }
  }
  async function saveManual(m: TMatch) {
    const body = { winner_id: manual.winner, legs1: manual.legs1, legs2: manual.legs2, outcome: manual.outcome };
    await actForce((f) => api.post(`${base}/results/${m.key}${fq(f)}`, body), manual.outcome ? 'Aufgabe/Disqualifikation eingetragen.' : 'Ergebnis eingetragen.');
    selectedKey = '';
  }
  async function link(m: TMatch, c: Candidate) {
    await actForce((f) => api.post(`${base}/results/${m.key}${fq(f)}`, { match_id: c.match_id }), 'Match zugeordnet.');
    selectedKey = '';
  }
  function clear(m: TMatch) {
    const what = m.match_id ? 'Zuordnung lösen? Das Match wird für dieses Turnier nicht mehr automatisch zugeordnet.' : 'Ergebnis löschen?';
    ask({
      title: m.match_id ? 'Zuordnung lösen' : 'Ergebnis löschen',
      text: what,
      okLabel: m.match_id ? 'Zuordnung lösen' : 'Löschen',
      danger: true,
      ok: async () => {
        await actForce((f) => api.del(`${base}/results/${m.key}${fq(f)}`), 'Ergebnis entfernt.');
        selectedKey = '';
      },
    });
  }

  // ---- Teilnehmer im laufenden Turnier ----

  // TS verengt view im Skript sonst auf null (Zuweisung erst in load()).
  const cur = (): TournamentView | null => view;
  const running = $derived(cur()?.status === 'running');
  const byes = $derived.by((): TMatch[] => {
    const v = cur();
    if (!v || v.status !== 'running') return [];
    return (v.main[0]?.matches ?? []).filter((m) => m.status === 'walkover' && m.b.placeholder === 'Freilos');
  });
  const swapCandidates = $derived.by((): TParticipant[] => (cur()?.standings ?? []).filter((p) => !p.out && !p.played && !p.withdrawn && !p.replaced));
  const inTournament = $derived(new Set((cur()?.standings ?? []).map((p) => p.player_id)));
  const pPath = (pid: number, what: string) => `${base}/players/${pid}/${what}`;

  async function openAction(a: PAction) {
    paction = a;
    pform = { playerId: 0, newName: '', swapWith: 0, reason: 'retired' };
    if ((a.kind === 'replace' || a.kind === 'late') && !allPlayers.length) {
      try {
        allPlayers = await api.get<{ id: number; display_name: string }[]>('/api/admin/players');
      } catch (e) {
        error = (e as Error).message;
      }
    }
  }

  function newcomerBody() {
    return pform.playerId ? { player_id: pform.playerId } : { new_name: pform.newName.trim() };
  }
  function newcomerName(): string {
    if (pform.playerId) return allPlayers.find((p) => p.id === pform.playerId)?.display_name ?? '';
    return pform.newName.trim();
  }
  const closePanel = () => {
    paction = null;
  };

  function confirmAction() {
    const a = paction;
    if (!a) return;
    if (a.kind === 'replace') {
      const who = newcomerName();
      ask({
        title: 'Spieler ersetzen',
        text: `${a.p.display_name} durch ${who} ersetzen? Gespielte Ergebnisse bleiben ${a.p.display_name}; offene und künftige Paarungen spielt ${who}.`,
        okLabel: 'Ersetzen',
        ok: () => act(() => api.post(pPath(a.p.player_id, 'replace'), newcomerBody()), `${who} ersetzt ${a.p.display_name}.`).then(closePanel),
      });
    } else if (a.kind === 'swap') {
      const other = cur()?.standings.find((p) => p.player_id === pform.swapWith);
      ask({
        title: 'Plätze tauschen',
        text: `${a.p.display_name} und ${other?.display_name ?? '?'} tauschen ihre Plätze im Turnierbaum?`,
        okLabel: 'Tauschen',
        ok: () => act(() => api.post(pPath(a.p.player_id, 'swap'), { with: pform.swapWith }), 'Plätze getauscht.').then(closePanel),
      });
    } else if (a.kind === 'withdraw') {
      const dq = pform.reason === 'dq';
      ask({
        title: dq ? 'Disqualifizieren' : 'Zurückziehen',
        text: `${a.p.display_name} ${dq ? 'disqualifizieren' : 'zurückziehen'}? Die offene und alle künftigen Paarungen gehen kampflos (w.o.) an den Gegner.`,
        okLabel: dq ? 'Disqualifizieren' : 'Zurückziehen',
        danger: true,
        ok: () =>
          act(() => api.post(pPath(a.p.player_id, 'withdraw'), { reason: pform.reason }), `${a.p.display_name} ${dq ? 'disqualifiziert' : 'zurückgezogen'}.`).then(closePanel),
      });
    } else {
      const who = newcomerName();
      ask({
        title: 'Nachmelden',
        text: `${who} auf das Freilos in Spiel ${a.m.no} setzen? ${a.m.a.display_name} spielt dann gegen ${who}.`,
        okLabel: 'Nachmelden',
        ok: () => act(() => api.post(`${base}/late/${a.m.key}`, newcomerBody()), `${who} nachgemeldet.`).then(closePanel),
      });
    }
  }

  function undoWithdraw(p: TParticipant) {
    ask({
      title: 'Rückzug rückgängig machen',
      text: `${p.display_name} spielt wieder mit. Die kampflosen Entscheidungen werden zurückgenommen.`,
      okLabel: 'Rückgängig',
      ok: () => actForce(() => api.del(pPath(p.player_id, 'withdraw')), `${p.display_name} spielt wieder mit.`),
    });
  }

  const outcomeText: Record<string, string> = { retired: 'Aufgabe', dq: 'Disqualifikation', withdrawn: 'kampflos (w.o.) nach Rückzug' };

  const sourceText: Record<string, string> = { auto: 'automatisch zugeordnet', link: 'von Hand zugeordnet', manual: 'von Hand eingetragen' };
</script>

{#if error}<p class="error">{error}</p>{/if}
{#if info}<p class="win">{info}</p>{/if}

{#if !view}
  {#if !error}<p class="muted">Lade …</p>{/if}
{:else}
  {@const v = view}
  <div class="row" style="justify-content: space-between">
    <h1 style="margin: 0">{v.name}</h1>
    {#if v.status !== 'draft'}
      <div class="row">
        <a class="pill" href="/turniere/{v.id}/beamer/baum" target="_blank" data-testid="bracket-beamer-link">Turnierbaum live (Beamer) ↗</a>
        <a class="pill" href="/turniere/{v.id}/beamer" target="_blank">Beamer-Ansicht ↗</a>
      </div>
    {/if}
  </div>
  <p class="muted">
    {statusText[v.status]} · {v.players} Spieler
    {#if v.started_at} · gestartet {dateTime(v.started_at)}{/if}
    {#if v.third_place} · mit Spiel um Platz 3{/if}
    {#if v.lucky_loser} · Lucky Loser steigt vor dem {v.ll_entry_name ?? '?'} ein{/if}
  </p>

  {#if v.champion}
    <div class="card brand-card champion">🏆 Turniersieger: <a href="/spieler/{v.champion.player_id}" data-testid="tournament-champion">{v.champion.display_name}</a></div>
  {/if}

  {#if admin}
    <div class="row" style="margin-bottom: 12px">
      {#if v.status === 'draft'}
        <button onclick={start} disabled={v.players < 2}>Auslosen & starten</button>
      {:else if v.status === 'running'}
        <button class="secondary" onclick={redraw}>Neu auslosen</button>
      {/if}
      <button class="secondary" onclick={() => (editing = !editing)}>{editing ? 'Bearbeiten schließen' : 'Bearbeiten'}</button>
      <button class="danger" onclick={remove}>Löschen</button>
    </div>
    {#if editing}
      <TournamentForm initial={v} submitLabel="Speichern" onsave={save} />
    {/if}
  {/if}

  {#if v.status === 'draft'}
    <h2>Angemeldet</h2>
    <div class="card row">
      {#each v.standings as p}<a class="pill" href="/spieler/{p.player_id}">{p.display_name}</a>{:else}<span class="muted">Noch niemand.</span>{/each}
    </div>
    <h2>Spielregeln</h2>
    <div class="card">
      {#each v.round_names as n, i}
        <div>{n}: <span class="muted">{ruleText(v.rules.rounds?.[Math.min(v.round_names.length - 1 - i, (v.rules.rounds?.length ?? 1) - 1)])}</span></div>
      {/each}
    </div>
  {:else}
    {#if v.next.length}
      <h2>Als Nächstes</h2>
      <div class="card next">
        {#each v.next as m (m.key)}
          <div><span class="muted">Spiel {m.no}</span> <strong>{m.a.display_name}</strong> <span class="muted">vs</span> <strong>{m.b.display_name}</strong> <span class="muted">· {ruleText(m.rule)}</span></div>
        {/each}
      </div>
    {/if}

    {#if admin}<p class="muted">Tipp: Auf ein Spiel klicken, um ein Ergebnis einzutragen, ein Match zuzuordnen oder eine Zuordnung zu lösen.</p>{/if}

    {#snippet editor(m: TMatch)}
      <div class="card editor">
        <div class="row" style="justify-content: space-between">
          <strong>Spiel {m.no}: {m.a.display_name} vs {m.b.display_name}</strong>
          <button class="secondary" onclick={() => (selectedKey = '')}>Schließen</button>
        </div>
        <p class="muted">{ruleText(m.rule)}</p>
        {#if m.status === 'done'}
          <p>
            Ergebnis {m.a.legs}:{m.b.legs}, {sourceText[m.source ?? ''] ?? ''}
            {#if m.match_id} · <a href="/match/{m.match_id}">Match ansehen</a>{/if}
          </p>
          {#if m.outcome}<p class="warn">{outcomeText[m.outcome]}</p>{/if}
          {#if m.warning}<p class="warn">⚠ {m.warning}</p>{/if}
          <button class="danger" onclick={() => clear(m)}>{m.match_id ? 'Zuordnung lösen' : 'Ergebnis löschen'}</button>
        {/if}
        <h3>Erfasstes Match zuordnen</h3>
        {#each candidates as c (c.match_id)}
          <div class="row cand">
            <span>{dateTime(c.played_at)} · {c.variant} · {c.score} · Sieger {c.winner}{#if c.linked_to} <span class="muted">(zugeordnet: {c.linked_to})</span>{/if}</span>
            <button class="secondary" onclick={() => link(m, c)} disabled={c.match_id === m.match_id}>Zuordnen</button>
          </div>
        {:else}
          <p class="muted">Kein beendetes Match der beiden seit Turnierstart.</p>
        {/each}
        <h3>Ergebnis von Hand</h3>
        <div class="filters">
          <label>Sieger
            <select value={manual.winner} onchange={(e) => setWinner(Number((e.target as HTMLSelectElement).value), m)}>
              <option value={0}>–</option>
              <option value={m.a.player_id}>{m.a.display_name}</option>
              <option value={m.b.player_id}>{m.b.display_name}</option>
            </select>
          </label>
          <label>Legs {m.a.display_name} <input type="number" min="0" bind:value={manual.legs1} style="width: 80px" /></label>
          <label>Legs {m.b.display_name} <input type="number" min="0" bind:value={manual.legs2} style="width: 80px" /></label>
          <label>Ausgang
            <select bind:value={manual.outcome} data-testid="result-outcome">
              <option value="">regulär</option>
              <option value="retired">Aufgabe des Verlierers</option>
              <option value="dq">Disqualifikation des Verlierers</option>
            </select>
          </label>
          <button onclick={() => saveManual(m)} disabled={!manual.winner} data-testid="result-save">Eintragen</button>
        </div>
      </div>
    {/snippet}

    <div data-testid="bracket">
    <h2>Turnierbaum</h2>
    <div class="card"><Bracket rounds={v.main} selected={selectedKey} onselect={admin ? select : undefined} /></div>
    {#if selected && selected.bracket === 'main'}{@render editor(selected)}{/if}

    {#if v.third}
      <h2>Spiel um Platz 3</h2>
      <div class="card"><Bracket rounds={[{ name: 'Platz 3', rule: v.third.rule, matches: [v.third] }]} selected={selectedKey} onselect={admin ? select : undefined} /></div>
      {#if selected && selected.bracket === 'third'}{@render editor(selected)}{/if}
    {/if}

    {#if llRounds.length}
      <h2>Lucky-Loser-Runde</h2>
      <div class="card"><Bracket rounds={llRounds} selected={selectedKey} onselect={admin ? select : undefined} /></div>
      {#if selected && (selected.bracket === 'll' || selected.bracket === 'playin')}{@render editor(selected)}{/if}
    {/if}
    </div>

    <h2>Platzierungen</h2>
    <div class="card table-wrap">
      <table>
        <thead><tr><th>Platz</th><th style="text-align:left">Spieler</th><th style="text-align:left">Erreicht</th>{#if admin && running}<th style="text-align:left">Aktionen</th>{/if}</tr></thead>
        <tbody>
          {#each v.standings as p (p.player_id)}
            <tr class:struck={p.replaced || !!p.withdrawn} data-testid="participant-row" data-player-id={p.player_id}>
              <td>{p.place || '–'}</td>
              <td style="text-align:left"><a href="/spieler/{p.player_id}" class="pname">{p.display_name}</a>{#if p.lucky_loser} <span class="pill" title="hat in der Lucky-Loser-Runde gespielt">LL</span>{/if}</td>
              <td style="text-align:left">{p.label}{#if p.replaced_by} → <a href="/spieler/{p.replaced_by.player_id}">{p.replaced_by.display_name}</a>{/if}</td>
              {#if admin && running}
                <td style="text-align:left">
                  <div class="row actions" data-testid="participant-actions">
                    {#if p.withdrawn}
                      <button class="secondary" onclick={() => undoWithdraw(p)} data-testid="participant-undo-withdraw">Rückgängig</button>
                    {:else if !p.out && !p.replaced}
                      <button class="secondary" onclick={() => openAction({ kind: 'replace', p })} data-testid="participant-replace">Ersetzen</button>
                      {#if !p.played && swapCandidates.length > 1}
                        <button class="secondary" onclick={() => openAction({ kind: 'swap', p })} data-testid="participant-swap">Tauschen</button>
                      {/if}
                      <button class="danger" onclick={() => openAction({ kind: 'withdraw', p })} data-testid="participant-withdraw">Zurückziehen</button>
                    {/if}
                  </div>
                </td>
              {/if}
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    {#if admin && running && byes.length}
      <h2>Freilose</h2>
      <div class="card next">
        {#each byes as m (m.key)}
          <div class="row" style="justify-content: space-between">
            <span><span class="muted">Spiel {m.no}</span> {m.a.display_name} hat ein Freilos</span>
            <button class="secondary" onclick={() => openAction({ kind: 'late', m })} data-testid="late-entry" data-key={m.key}>Nachmelden</button>
          </div>
        {/each}
      </div>
    {/if}

    {#if paction}
      {@const a = paction}
      <div class="card editor" data-testid="participant-panel" data-kind={a.kind}>
        <div class="row" style="justify-content: space-between">
          <strong>
            {#if a.kind === 'replace'}{a.p.display_name} ersetzen
            {:else if a.kind === 'swap'}{a.p.display_name}: Platz tauschen
            {:else if a.kind === 'withdraw'}{a.p.display_name} zurückziehen
            {:else}Nachmelden auf das Freilos in Spiel {a.m.no}{/if}
          </strong>
          <button class="secondary" onclick={closePanel} data-testid="participant-panel-close">Schließen</button>
        </div>
        {#if a.kind === 'replace' || a.kind === 'late'}
          <div class="filters">
            <label>Vorhandener Spieler
              <select bind:value={pform.playerId} data-testid="newcomer-select">
                <option value={0}>– neuer Name –</option>
                {#each allPlayers.filter((x) => !inTournament.has(x.id)) as x (x.id)}<option value={x.id}>{x.display_name}</option>{/each}
              </select>
            </label>
            {#if !pform.playerId}
              <label>Neuer Name <input bind:value={pform.newName} data-testid="newcomer-name" maxlength="100" /></label>
            {/if}
            <button onclick={confirmAction} disabled={!pform.playerId && !pform.newName.trim()} data-testid="participant-panel-submit">Weiter</button>
          </div>
          {#if a.kind === 'replace'}<p class="muted">Bereits gespielte Ergebnisse bleiben {a.p.display_name} zugeordnet.</p>{/if}
        {:else if a.kind === 'swap'}
          <div class="filters">
            <label>Tauschen mit
              <select bind:value={pform.swapWith} data-testid="swap-select">
                <option value={0}>–</option>
                {#each swapCandidates.filter((x) => x.player_id !== a.p.player_id) as x (x.player_id)}<option value={x.player_id}>{x.display_name}</option>{/each}
              </select>
            </label>
            <button onclick={confirmAction} disabled={!pform.swapWith} data-testid="participant-panel-submit">Weiter</button>
          </div>
          <p class="muted">Nur möglich, solange beide noch nicht gespielt haben.</p>
        {:else if a.kind === 'withdraw'}
          <div class="filters">
            <label>Grund
              <select bind:value={pform.reason} data-testid="withdraw-reason">
                <option value="retired">Aufgabe / Rückzug</option>
                <option value="dq">Disqualifikation</option>
              </select>
            </label>
            <button class="danger" onclick={confirmAction} data-testid="participant-panel-submit">Weiter</button>
          </div>
        {/if}
      </div>
    {/if}

    {#if v.stats && v.stats.rows.length}
      {@const s = v.stats}
      <h2>Turnier-Statistik</h2>
      {#if s.best_average || s.most_180 || s.highest_checkout}
      <div class="card stats-grid">
        <div class="stat"><div class="label">Bester Average</div><div class="value">{num(s.best_average?.value)}</div><div class="muted">{s.best_average?.display_name ?? ''}</div></div>
        <div class="stat"><div class="label">Bestes Match</div><div class="value">{num(s.best_match_average?.value)}</div><div class="muted">{s.best_match_average?.display_name ?? ''}</div></div>
        <div class="stat"><div class="label">Meiste 180er</div><div class="value">{s.most_180?.value ?? '–'}</div><div class="muted">{s.most_180?.display_name ?? ''}</div></div>
        <div class="stat"><div class="label">Höchstes Finish</div><div class="value">{s.highest_checkout?.value ?? '–'}</div><div class="muted">{s.highest_checkout?.display_name ?? ''}</div></div>
      </div>
      {/if}
      <div class="card table-wrap">
        <table>
          <thead><tr><th>Spieler</th><th>Sp</th><th>S</th><th>Legs</th><th>Avg</th><th>Bestes</th><th>180</th><th>140+</th><th>High CO</th></tr></thead>
          <tbody>
            {#each s.rows as r (r.player_id)}
              <tr>
                <td><a href="/spieler/{r.player_id}">{r.display_name}</a></td>
                <td>{r.matches}</td><td>{r.wins}</td><td>{r.legs_won}:{r.legs_lost}</td>
                <td>{num(r.average)}</td><td>{num(r.best_match_average)}</td>
                <td>{r.count_180}</td><td>{r.count_140plus}</td><td>{r.highest_checkout || '–'}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      <p class="muted">Wurfstatistik nur aus erfassten Matches; von Hand eingetragene Ergebnisse zählen bei Siegen und Legs.</p>
    {/if}
  {/if}
{/if}

{#if dialog}
  {@const d = dialog}
  <div class="overlay" role="presentation" onclick={(e) => { if (e.target === e.currentTarget) dialog = null; }}>
    <div class="card dialog" role="dialog" aria-modal="true" aria-labelledby="dlg-title" data-testid="confirm-dialog">
      <h3 id="dlg-title">{d.title}</h3>
      <p>{d.text}</p>
      {#if d.list?.length}
        <ul data-testid="confirm-dialog-list">{#each d.list as item}<li>{item}</li>{/each}</ul>
      {/if}
      <div class="row" style="justify-content: flex-end">
        <button class="secondary" onclick={() => (dialog = null)} data-testid="confirm-cancel">{d.ok ? 'Abbrechen' : 'Schließen'}</button>
        {#if d.ok}
          <button class={d.danger ? 'danger' : ''} onclick={dialogOk} disabled={dialogBusy} data-testid="confirm-ok">{d.okLabel ?? 'OK'}</button>
        {/if}
      </div>
    </div>
  </div>
{/if}

<style>
  .champion { font-family: var(--font-heading); font-size: 1.4rem; font-weight: 700; }
  .next { display: flex; flex-direction: column; gap: 6px; }
  .editor { border-color: color-mix(in srgb, var(--accent) 45%, transparent); }
  .editor h3 { font-size: 0.95rem; margin: 12px 0 6px; }
  .cand { justify-content: space-between; padding: 4px 0; border-bottom: 1px solid var(--border); }
  .warn { color: #c77700; }
  .actions { gap: 6px; flex-wrap: wrap; }
  .actions button { padding: 4px 10px; min-height: 0; font-size: 0.82rem; }
  tr.struck .pname { text-decoration: line-through; color: var(--muted); }
  .overlay { position: fixed; inset: 0; background: rgba(0, 0, 0, 0.45); display: flex; align-items: center; justify-content: center; padding: 16px; z-index: 50; }
  .dialog { max-width: 520px; width: 100%; }
  .dialog h3 { margin-top: 0; }
  .dialog ul { margin: 0 0 12px; padding-left: 20px; }
</style>
