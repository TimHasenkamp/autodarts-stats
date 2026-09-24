<script lang="ts">
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { api, type Candidate, type TMatch, type TRound, type TournamentInput, type TournamentView } from '$lib/api';
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
  let manual = $state({ winner: 0, legs1: 0, legs2: 0 });

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

  const base = $derived(`/api/admin/tournaments/${id}`);

  async function select(m: TMatch) {
    if (!admin) return;
    if (selectedKey === m.key) {
      selectedKey = '';
      return;
    }
    selectedKey = m.key;
    manual = { winner: 0, legs1: m.rule.first_to, legs2: 0 };
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
    await act(() => api.post(`${base}/results/${m.key}`, { winner_id: manual.winner, legs1: manual.legs1, legs2: manual.legs2 }), 'Ergebnis eingetragen.');
    selectedKey = '';
  }
  async function link(m: TMatch, c: Candidate) {
    await act(() => api.post(`${base}/results/${m.key}`, { match_id: c.match_id }), 'Match zugeordnet.');
    selectedKey = '';
  }
  async function clear(m: TMatch) {
    const what = m.match_id ? 'Zuordnung lösen? Das Match wird für dieses Turnier nicht mehr automatisch zugeordnet.' : 'Ergebnis löschen?';
    if (!confirm(what + ' Spätere Ergebnisse, die davon abhängen, verfallen.')) return;
    await act(() => api.del(`${base}/results/${m.key}`), 'Ergebnis entfernt.');
    selectedKey = '';
  }

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
    {#if v.status !== 'draft'}<a class="pill" href="/turniere/{v.id}/beamer" target="_blank">Beamer-Ansicht ↗</a>{/if}
  </div>
  <p class="muted">
    {statusText[v.status]} · {v.players} Spieler
    {#if v.started_at} · gestartet {dateTime(v.started_at)}{/if}
    {#if v.third_place} · mit Spiel um Platz 3{/if}
    {#if v.lucky_loser} · Lucky Loser steigt vor dem {v.ll_entry_name ?? '?'} ein{/if}
  </p>

  {#if v.champion}
    <div class="card brand-card champion">🏆 Turniersieger: <a href="/spieler/{v.champion.player_id}">{v.champion.display_name}</a></div>
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
          <button onclick={() => saveManual(m)} disabled={!manual.winner}>Eintragen</button>
        </div>
      </div>
    {/snippet}

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

    <h2>Platzierungen</h2>
    <div class="card table-wrap">
      <table>
        <thead><tr><th>Platz</th><th style="text-align:left">Spieler</th><th style="text-align:left">Erreicht</th></tr></thead>
        <tbody>
          {#each v.standings as p (p.player_id)}
            <tr>
              <td>{p.place || '–'}</td>
              <td style="text-align:left"><a href="/spieler/{p.player_id}">{p.display_name}</a>{#if p.lucky_loser} <span class="pill" title="hat in der Lucky-Loser-Runde gespielt">LL</span>{/if}</td>
              <td style="text-align:left">{p.label}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

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

<style>
  .champion { font-family: var(--font-heading); font-size: 1.4rem; font-weight: 700; }
  .next { display: flex; flex-direction: column; gap: 6px; }
  .editor { border-color: color-mix(in srgb, var(--accent) 45%, transparent); }
  .editor h3 { font-size: 0.95rem; margin: 12px 0 6px; }
  .cand { justify-content: space-between; padding: 4px 0; border-bottom: 1px solid var(--border); }
  .warn { color: #c77700; }
</style>
