<script lang="ts">
  import { page } from '$app/state';
  import { api, type MatchDetail } from '$lib/api';
  import { dateTime, num, pct } from '$lib/format';

  let m: MatchDetail | null = $state(null);
  let error = $state('');
  $effect(() => {
    api.get<MatchDetail>(`/api/matches/${page.params.id}`).then((d) => (m = d)).catch((e) => (error = e.message));
  });
</script>

{#if error}
  <p class="error">{error}</p>
{:else if !m}
  <p class="muted">Lade …</p>
{:else}
  <h1>{m.players.map((p) => p.display_name).join(' vs ')}</h1>
  <p class="muted">{dateTime(m.played_at)} · {m.variant || 'unbekannte Variante'}{#if m.board_name} · {m.board_name}{/if}{#if !m.finished} · <span class="pill">läuft</span>{/if}</p>

  <div class="card table-wrap">
    <table>
      <thead><tr><th>Spieler</th><th>Legs</th><th>Avg</th><th>First 9</th><th>CO %</th><th>High CO</th><th>180</th><th>140+</th><th>100+</th></tr></thead>
      <tbody>
        {#each m.players as p}
          <tr>
            <td><a href="/spieler/{p.player_id}" class:win={p.won}>{p.display_name}</a>{#if p.won} 🏆{/if}</td>
            <td>{p.legs_won}</td><td>{num(p.average)}</td><td>{num(p.first9_avg)}</td><td>{pct(p.checkout_rate)}</td>
            <td>{p.highest_checkout || '–'}</td><td>{p.count_180}</td><td>{p.count_140plus}</td><td>{p.count_100plus}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>

  {#if m.pending.length}
    <div class="card">
      <strong>Wartet auf Freigabe:</strong>
      {#each m.pending as ps}<span class="pill">{ps.name}</span> <span class="muted">({ps.reason})</span>{/each}
    </div>
  {/if}

  <h2>Legs</h2>
  {#if m.legs.length === 0}
    <p class="muted">Noch keine abgeschlossenen Legs erfasst.</p>
  {:else}
    <div class="card table-wrap">
      <table>
        <thead><tr><th>Leg</th><th style="text-align:left">Spieler</th><th>Darts</th><th>Avg</th><th>First 9</th><th>Finish</th><th>180</th></tr></thead>
        <tbody>
          {#each m.legs as l}
            <tr>
              <td>{m.legs.some((x) => x.set > 1) ? `${l.set}.` : ''}{l.leg}</td>
              <td style="text-align:left" class:win={l.won}>{l.display_name}</td>
              <td>{l.darts}</td><td>{num(l.average)}</td><td>{num(l.first9_avg)}</td><td>{l.checkout || '–'}</td><td>{l.count_180}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{/if}
