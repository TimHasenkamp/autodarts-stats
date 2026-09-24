<script lang="ts">
  import { api, ApiError, type MatchSummary } from '$lib/api';
  import { dateTime } from '$lib/format';

  interface AdminPlayer { id: number; display_name: string; normalized_name: string; autodarts_user_id?: string; aliases: string[]; protected: boolean; chips: number; matches: number; legs: number }
  interface Board { id: number; name: string; created_at: string; last_seen_at?: string; matches: number }
  interface Unparsed { id: number; kind: string; url: string; reason: string; size: number; preview: string; received_at: string }
  interface Chip { id: number; player_id: number; display_name: string; label: string; created_at: string; last_used_at?: string }
  interface UnknownChip { uid_hash: string; uid_hint: string; board_name?: string; first_seen_at: string; last_seen_at: string; seen_count: number }
  interface Checkin { id: number; player_id: number; display_name: string; board_id: number; board_name?: string; token: string; game_name: string; created_at: string; expires_at: string }
  interface Pending { match_id: number; player_index: number; name: string; user_id?: string; suggested_id?: number; suggested_name?: string; reason: string; created_at: string; played_at: string; variant: string; board_name?: string; opponents: string; finished: boolean }

  let me: { admin: boolean; configured: boolean } | null = $state(null);
  let password = $state('');
  let error = $state('');
  let info = $state('');
  let players: AdminPlayer[] = $state([]);
  let boards: Board[] = $state([]);
  let unparsed: Unparsed[] = $state([]);
  let newBoard = $state('');
  let newKey = $state('');
  let q = $state('');
  let mergeTarget: Record<number, number> = $state({});
  let aliasInput: Record<number, string> = $state({});
  let renameInput: Record<number, string> = $state({});
  let chips: Chip[] = $state([]);
  let unknownChips: UnknownChip[] = $state([]);
  let checkins: Checkin[] = $state([]);
  let pending: Pending[] = $state([]);
  let chipAssign: Record<string, { player_id: number; new_name: string; label: string }> = $state({});
  let pendingAssign: Record<string, number> = $state({});
  let manualCheckin = $state({ player_id: 0, board_id: 0 });
  let newPlayer = $state({ display_name: '', protected: true });
  let matches: MatchSummary[] = $state([]);

  let filtered = $derived(players.filter((p) => p.display_name.toLowerCase().includes(q.toLowerCase()) || p.aliases.some((a) => a.includes(q.toLowerCase()))));

  async function guard<T>(fn: () => Promise<T>): Promise<T | undefined> {
    error = '';
    info = '';
    try {
      return await fn();
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) me = { admin: false, configured: true };
      error = (e as Error).message;
    }
  }

  async function loadAll() {
    await guard(async () => {
      const [pl, bo, un, ch, ci, pe] = await Promise.all([
        api.get<AdminPlayer[]>('/api/admin/players'),
        api.get<Board[]>('/api/admin/boards'),
        api.get<Unparsed[]>('/api/admin/unparsed'),
        api.get<{ chips: Chip[]; unknown: UnknownChip[] }>('/api/admin/chips'),
        api.get<Checkin[]>('/api/admin/checkins'),
        api.get<Pending[]>('/api/admin/pending'),
      ]);
      matches = await api.get<MatchSummary[]>('/api/matches?limit=50');
      players = pl; boards = bo; unparsed = un; chips = ch.chips; unknownChips = ch.unknown; checkins = ci; pending = pe;
      for (const p of pe) {
        const k = `${p.match_id}/${p.player_index}`;
        if (!(k in pendingAssign)) pendingAssign[k] = p.suggested_id ?? 0;
      }
    });
  }

  api.get<{ admin: boolean; configured: boolean }>('/api/admin/me').then((m) => {
    me = m;
    if (m.admin) loadAll();
  });

  async function login() {
    await guard(async () => {
      await api.post('/api/admin/login', { password });
      password = '';
      me = { admin: true, configured: true };
      await loadAll();
    });
  }
  async function logout() {
    await api.post('/api/admin/logout');
    me = { admin: false, configured: true };
  }
  async function rename(p: AdminPlayer) {
    const name = (renameInput[p.id] ?? '').trim();
    if (!name) return;
    await guard(async () => { await api.post(`/api/admin/players/${p.id}/rename`, { display_name: name }); renameInput[p.id] = ''; await loadAll(); });
  }
  async function addAlias(p: AdminPlayer) {
    const alias = (aliasInput[p.id] ?? '').trim();
    if (!alias) return;
    await guard(async () => { await api.post(`/api/admin/players/${p.id}/aliases`, { alias }); aliasInput[p.id] = ''; await loadAll(); });
  }
  async function delAlias(p: AdminPlayer, alias: string) {
    await guard(async () => { await api.del(`/api/admin/players/${p.id}/aliases/${encodeURIComponent(alias)}`); await loadAll(); });
  }
  async function merge(p: AdminPlayer) {
    const into = mergeTarget[p.id];
    if (!into) return;
    const target = players.find((x) => x.id === into);
    if (!confirm(`„${p.display_name}“ in „${target?.display_name}“ zusammenführen? Alle Matches und Legs werden übertragen, „${p.display_name}“ wird gelöscht.`)) return;
    await guard(async () => { await api.post(`/api/admin/players/${p.id}/merge`, { into }); await loadAll(); });
  }
  async function delPlayer(p: AdminPlayer) {
    if (!confirm(`„${p.display_name}“ samt ${p.matches} Matches und allen Aliassen endgültig löschen?`)) return;
    await guard(async () => { await api.del(`/api/admin/players/${p.id}`); await loadAll(); });
  }
  async function createBoard() {
    await guard(async () => {
      const r = await api.post<{ api_key: string; name: string }>('/api/admin/boards', { name: newBoard });
      newKey = r.api_key;
      newBoard = '';
      await loadAll();
    });
  }
  async function delBoard(b: Board) {
    if (!confirm(`Board „${b.name}“ entfernen? Der API-Key wird ungültig, Matches bleiben erhalten.`)) return;
    await guard(async () => { await api.del(`/api/admin/boards/${b.id}`); await loadAll(); });
  }
  async function reprocess() {
    await guard(async () => {
      const r = await api.post<{ matches: number }>('/api/admin/reprocess');
      info = `${r.matches} Matches neu berechnet.`;
      await loadAll();
    });
  }
  async function toggleProtect(p: AdminPlayer) {
    await guard(async () => { await api.post(`/api/admin/players/${p.id}/protect`, { protected: !p.protected }); await loadAll(); });
  }
  async function createPlayer() {
    await guard(async () => { await api.post('/api/admin/players', newPlayer); newPlayer = { display_name: '', protected: true }; await loadAll(); });
  }
  function chipForm(hash: string) {
    if (!chipAssign[hash]) chipAssign[hash] = { player_id: 0, new_name: '', label: '' };
    return chipAssign[hash];
  }
  async function assignChip(u: UnknownChip) {
    const f = chipForm(u.uid_hash);
    if (!f.player_id && !f.new_name.trim()) { error = 'Spieler wählen oder neuen Namen eingeben.'; return; }
    await guard(async () => {
      await api.post('/api/admin/chips', { uid_hash: u.uid_hash, player_id: f.player_id || undefined, new_name: f.new_name.trim() || undefined, label: f.label });
      delete chipAssign[u.uid_hash];
      await loadAll();
    });
  }
  async function forgetUnknown(u: UnknownChip) {
    await guard(async () => { await api.del(`/api/admin/chips/unknown/${u.uid_hash}`); await loadAll(); });
  }
  async function delChip(c: Chip) {
    if (!confirm(`Chip von „${c.display_name}“ entfernen?`)) return;
    await guard(async () => { await api.del(`/api/admin/chips/${c.id}`); await loadAll(); });
  }
  async function endCheckin(c: Checkin) {
    await guard(async () => { await api.del(`/api/admin/checkins/${c.id}`); await loadAll(); });
  }
  async function doManualCheckin() {
    await guard(async () => {
      const r = await api.post<Checkin>('/api/admin/checkins', manualCheckin);
      info = `Eingecheckt: ${r.game_name}`;
      await loadAll();
    });
  }
  async function resolvePending(p: Pending, ignore: boolean) {
    const k = `${p.match_id}/${p.player_index}`;
    const body = ignore ? { ignore: true } : { player_id: pendingAssign[k] };
    if (!ignore && !pendingAssign[k]) { error = 'Bitte Spieler wählen.'; return; }
    await guard(async () => { await api.post(`/api/admin/pending/${p.match_id}/${p.player_index}`, body); await loadAll(); });
  }
  async function reprocessUnparsed() {
    await guard(async () => {
      const r = await api.post<{ recognized: number; total: number }>('/api/admin/unparsed/reprocess');
      info = `${r.recognized} von ${r.total} unerkannten Events jetzt erkannt.`;
      await loadAll();
    });
  }
  async function delMatch(m: MatchSummary) {
    const wer = m.players.map((p) => p.display_name).join(' vs ') || 'ohne Spieler';
    if (!confirm(`Match vom ${dateTime(m.played_at)} (${wer}) endgültig löschen? Die Rohdaten gehen mit verloren.`)) return;
    await guard(async () => { await api.del(`/api/admin/matches/${m.match_id}`); await loadAll(); });
  }
  async function clearUnparsed() {
    await guard(async () => { await api.del('/api/admin/unparsed'); await loadAll(); });
  }
</script>

<h1>Admin</h1>
{#if error}<p class="error">{error}</p>{/if}
{#if info}<p class="win">{info}</p>{/if}

{#if !me}
  <p class="muted">Lade …</p>
{:else if !me.configured}
  <p class="error">Auf dem Server ist kein ADMIN_PASSWORD gesetzt.</p>
{:else if !me.admin}
  <form class="card filters" onsubmit={(e) => { e.preventDefault(); login(); }}>
    <label>Passwort <input type="password" bind:value={password} autocomplete="current-password" /></label>
    <button type="submit">Anmelden</button>
  </form>
{:else}
  <div class="row" style="margin-bottom: 12px">
    <button class="secondary" onclick={reprocess}>Statistiken neu berechnen</button>
    <button class="secondary" onclick={logout}>Abmelden</button>
  </div>

  <p class="muted">Turniere werden unter <a href="/turniere">Turniere</a> angelegt und verwaltet (als Admin angemeldet).</p>

  <h2>Boards</h2>
  <div class="card">
    <form class="filters" onsubmit={(e) => { e.preventDefault(); createBoard(); }}>
      <label>Neues Board <input bind:value={newBoard} placeholder="Wohnzimmer" /></label>
      <button type="submit" disabled={!newBoard.trim()}>Anlegen</button>
    </form>
    {#if newKey}
      <p>API-Key (wird nur jetzt angezeigt): <code style="user-select:all">{newKey}</code></p>
    {/if}
    <div class="table-wrap">
      <table>
        <thead><tr><th>Name</th><th>Matches</th><th>Zuletzt gesehen</th><th></th></tr></thead>
        <tbody>
          {#each boards as b}
            <tr><td>{b.name}</td><td>{b.matches}</td><td>{dateTime(b.last_seen_at)}</td><td><button class="danger" onclick={() => delBoard(b)}>Entfernen</button></td></tr>
          {:else}
            <tr><td colspan="4" class="muted">Noch kein Board. Lege eines an und trage den Key in der Extension ein.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </div>

  <h2>Freigabe <span class="muted">({pending.length})</span></h2>
  <div class="card">
    <p class="muted">Matches, in denen ein geschützter Name ohne gültigen Check-in-Code auftaucht. Sie zählen erst nach Zuordnung.</p>
    {#each pending as p (p.match_id + '/' + p.player_index)}
      {@const k = `${p.match_id}/${p.player_index}`}
      <div class="player">
        <div class="row" style="justify-content: space-between">
          <div>
            <strong>{p.name}</strong>{#if p.user_id} <span class="pill">Account</span>{/if}
            <span class="muted">· <a href="/match/{p.match_id}">{dateTime(p.played_at)}</a> · {p.variant}{#if p.board_name} · {p.board_name}{/if}{#if p.opponents} · gegen {p.opponents}{/if}{#if !p.finished} · läuft{/if}</span>
            <div class="muted">{p.reason}</div>
          </div>
        </div>
        <div class="filters" style="margin-top: 6px">
          <label>Zuordnen zu
            <select bind:value={pendingAssign[k]}>
              <option value={0}>–</option>
              {#each players as t}<option value={t.id}>{t.display_name}{t.id === p.suggested_id ? ' (vorgeschlagen)' : ''}</option>{/each}
            </select>
          </label>
          <button onclick={() => resolvePending(p, false)} disabled={!pendingAssign[k]}>Zuordnen</button>
          <button class="danger" onclick={() => resolvePending(p, true)}>Verwerfen</button>
        </div>
      </div>
    {:else}
      <p class="muted">Nichts offen.</p>
    {/each}
  </div>

  <h2>Check-ins <span class="muted">({checkins.length} aktiv)</span></h2>
  <div class="card">
    <div class="table-wrap">
      <table>
        <thead><tr><th>Spielname</th><th style="text-align:left">Board</th><th>Seit</th><th>Bis</th><th></th></tr></thead>
        <tbody>
          {#each checkins as c (c.id)}
            <tr>
              <td><code style="user-select:all">{c.game_name}</code></td>
              <td style="text-align:left">{c.board_name}</td>
              <td>{dateTime(c.created_at)}</td><td>{dateTime(c.expires_at)}</td>
              <td><button class="secondary" onclick={() => endCheckin(c)}>Beenden</button></td>
            </tr>
          {:else}
            <tr><td colspan="5" class="muted">Niemand eingecheckt.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
    <form class="filters" style="margin-top: 8px" onsubmit={(e) => { e.preventDefault(); doManualCheckin(); }}>
      <label>Manuell einchecken
        <select bind:value={manualCheckin.player_id}>
          <option value={0}>Spieler …</option>
          {#each players as t}<option value={t.id}>{t.display_name}</option>{/each}
        </select>
      </label>
      <label>Board
        <select bind:value={manualCheckin.board_id}>
          <option value={0}>Board …</option>
          {#each boards as b}<option value={b.id}>{b.name}</option>{/each}
        </select>
      </label>
      <button type="submit" disabled={!manualCheckin.player_id || !manualCheckin.board_id}>Einchecken</button>
    </form>
  </div>

  <h2>Chips</h2>
  <div class="card">
    {#if unknownChips.length}
      <h3 style="margin: 0 0 6px; font-size: 1rem">Unbekannte Chips</h3>
      <p class="muted">Wurden am Board eingestempelt, gehören aber noch niemandem. Bestehenden Spieler wählen oder neuen (geschützten) Spieler anlegen.</p>
      {#each unknownChips as u (u.uid_hash)}
        {@const f = chipForm(u.uid_hash)}
        <div class="player">
          <div><strong>Chip {u.uid_hint}</strong> <span class="muted">· zuletzt {dateTime(u.last_seen_at)}{#if u.board_name} an {u.board_name}{/if} · {u.seen_count}×</span></div>
          <div class="filters" style="margin-top: 6px">
            <label>Spieler
              <select bind:value={f.player_id}>
                <option value={0}>– neu anlegen –</option>
                {#each players as t}<option value={t.id}>{t.display_name}</option>{/each}
              </select>
            </label>
            <label>Neuer Name <input bind:value={f.new_name} placeholder="nur wenn neu" disabled={!!f.player_id} /></label>
            <label>Bezeichnung <input bind:value={f.label} placeholder="z.B. Firmenausweis" /></label>
            <button onclick={() => assignChip(u)}>Zuordnen</button>
            <button class="secondary" onclick={() => forgetUnknown(u)}>Vergessen</button>
          </div>
        </div>
      {/each}
    {/if}
    <div class="table-wrap" style="margin-top: 8px">
      <table>
        <thead><tr><th>Spieler</th><th style="text-align:left">Bezeichnung</th><th>Registriert</th><th>Zuletzt benutzt</th><th></th></tr></thead>
        <tbody>
          {#each chips as c (c.id)}
            <tr>
              <td><a href="/spieler/{c.player_id}">{c.display_name}</a></td>
              <td style="text-align:left">{c.label || '–'}</td>
              <td>{dateTime(c.created_at)}</td><td>{dateTime(c.last_used_at)}</td>
              <td><button class="danger" onclick={() => delChip(c)}>Entfernen</button></td>
            </tr>
          {:else}
            <tr><td colspan="5" class="muted">Noch kein Chip registriert. Chip am Board einstempeln, dann erscheint er oben.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </div>

  <h2>Spieler</h2>
  <div class="card">
    <form class="filters" style="margin-bottom: 10px" onsubmit={(e) => { e.preventDefault(); createPlayer(); }}>
      <label>Neuer Spieler <input bind:value={newPlayer.display_name} placeholder="Name" /></label>
      <label style="flex-direction: row; align-items: center; gap: 6px"><input type="checkbox" bind:checked={newPlayer.protected} style="width:auto;min-height:0" /> geschützt</label>
      <button type="submit" disabled={!newPlayer.display_name.trim()}>Anlegen</button>
    </form>
    <input type="search" placeholder="Suchen …" bind:value={q} style="width:100%; margin-bottom: 8px" />
    {#each filtered as p (p.id)}
      <div class="player">
        <div class="row" style="justify-content: space-between">
          <div>
            <strong>{p.display_name}</strong>
            {#if p.protected}<span class="pill" title="Matches unter diesem Namen zählen nur mit Check-in-Code oder Account">🔒 geschützt</span>{/if}
            <span class="muted">#{p.id} · {p.matches} Matches · {p.legs} Legs{#if p.autodarts_user_id} · Account{/if}{#if p.chips} · {p.chips} Chip{p.chips > 1 ? 's' : ''}{/if}</span>
          </div>
          <div class="row">
            <button class="secondary" onclick={() => toggleProtect(p)}>{p.protected ? 'Schutz aufheben' : 'Schützen'}</button>
            <button class="danger" onclick={() => delPlayer(p)}>Löschen</button>
          </div>
        </div>
        <div class="row" style="margin-top: 6px">
          {#each p.aliases as a}
            <span class="pill">{a}{#if a !== p.normalized_name} <button class="x" title="Alias entfernen" onclick={() => delAlias(p, a)}>×</button>{/if}</span>
          {/each}
        </div>
        <div class="filters" style="margin-top: 8px">
          <label>Umbenennen <input bind:value={renameInput[p.id]} placeholder={p.display_name} /></label>
          <button class="secondary" onclick={() => rename(p)}>OK</button>
          <label>Alias hinzufügen <input bind:value={aliasInput[p.id]} placeholder="z.B. Timmy" /></label>
          <button class="secondary" onclick={() => addAlias(p)}>OK</button>
          <label>Zusammenführen in
            <select bind:value={mergeTarget[p.id]}>
              <option value={0}>–</option>
              {#each players.filter((x) => x.id !== p.id) as t}<option value={t.id}>{t.display_name}</option>{/each}
            </select>
          </label>
          <button class="secondary" onclick={() => merge(p)} disabled={!mergeTarget[p.id]}>OK</button>
        </div>
      </div>
    {:else}
      <p class="muted">Keine Spieler.</p>
    {/each}
  </div>

  <h2>Matches <span class="muted">({matches.length})</span></h2>
  <div class="card">
    <p class="muted">
      Ein Match, das dauerhaft auf „läuft“ steht, hat nie einen Endstand bekommen. Das lässt sich
      nachträglich nicht reparieren, weil die fehlenden Stände nirgends gespeichert sind. Solche
      Matches hier löschen.
    </p>
    <div class="table-wrap">
      <table>
        <thead><tr><th>Datum</th><th style="text-align:left">Variante</th><th style="text-align:left">Spieler</th><th>Legs</th><th style="text-align:left">Status</th><th></th></tr></thead>
        <tbody>
          {#each matches as m (m.match_id)}
            <tr>
              <td><a href="/match/{m.match_id}">{dateTime(m.played_at)}</a></td>
              <td style="text-align:left">{m.variant || '–'}</td>
              <td style="text-align:left">{m.players.map((p) => p.display_name).join(' vs ') || '–'}</td>
              <td>{m.players.map((p) => p.legs_won).join(':') || '–'}</td>
              <td style="text-align:left">{#if m.finished}beendet{:else}<span class="pill">läuft</span>{/if}</td>
              <td>
                <a href="/api/admin/matches/{m.match_id}/raw" target="_blank" class="pill">Rohdaten</a>
                <button class="danger" onclick={() => delMatch(m)}>Löschen</button>
              </td>
            </tr>
          {:else}
            <tr><td colspan="6" class="muted">Noch keine Matches erfasst.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </div>

  <h2>Unerkannte Events <span class="muted">({unparsed.length})</span></h2>
  <div class="card">
    <p class="muted">Payloads, die kein Parser verstanden hat. Nützlich, um das echte Autodarts-Format zu finden: JSON ansehen, unter <code>testdata/</code> ablegen.</p>
    {#if unparsed.length}
      <button class="secondary" onclick={reprocessUnparsed}>Erneut verarbeiten</button>
      <button class="secondary" onclick={clearUnparsed}>Alle löschen</button>
      <div class="table-wrap" style="margin-top:8px">
        <table>
          <thead><tr><th>Zeit</th><th style="text-align:left">Art</th><th style="text-align:left">URL</th><th>Größe</th><th style="text-align:left">Anfang</th></tr></thead>
          <tbody>
            {#each unparsed as u}
              <tr>
                <td><a href="/api/admin/unparsed/{u.id}" target="_blank">{dateTime(u.received_at)}</a></td>
                <td style="text-align:left">{u.kind}</td>
                <td style="text-align:left; max-width: 260px; overflow: hidden; text-overflow: ellipsis" title={u.url}>{u.url}</td>
                <td>{Math.round(u.size / 1024)} kB</td>
                <td style="text-align:left; max-width: 300px; overflow: hidden; text-overflow: ellipsis; font-family: monospace; font-size: 0.75rem">{u.preview}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </div>
{/if}

<style>
  .player { padding: 10px 0; border-bottom: 1px solid var(--border); }
  .player:last-child { border-bottom: none; }
  .x { background: none; color: var(--muted); padding: 0 2px; min-height: 0; border: none; font-size: 1rem; line-height: 1; }
</style>
