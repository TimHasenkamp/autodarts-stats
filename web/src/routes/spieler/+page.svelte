<script lang="ts">
  import { api, type PlayerRow } from '$lib/api';
  import { date, num } from '$lib/format';

  let rows: PlayerRow[] = $state([]);
  let q = $state('');
  let error = $state('');
  api.get<PlayerRow[]>('/api/players').then((r) => (rows = r)).catch((e) => (error = e.message));
  let filtered = $derived(rows.filter((r) => r.display_name.toLowerCase().includes(q.toLowerCase())));
  let anyOpen = $derived(rows.some((r) => r.open_matches > 0));
</script>

<h1>Spieler</h1>
<div class="card">
  <input type="search" placeholder="Suchen …" bind:value={q} style="width:100%" />
</div>
{#if error}<p class="error">{error}</p>{/if}
<div class="card table-wrap">
  <table>
    <thead><tr><th>Name</th><th>Matches</th><th>Siege</th><th>Avg</th><th>180</th><th>Zuletzt</th></tr></thead>
    <tbody>
      {#each filtered as r}
        <tr>
          <td>
            <a href="/spieler/{r.player_id}">{r.display_name}</a>
            {#if r.open_matches > 0}<span class="pill" title="Match läuft noch, zählt erst nach dem Ende">läuft</span>{/if}
          </td>
          <td>{r.matches}</td><td>{r.wins}</td><td>{num(r.average)}</td><td>{r.count_180}</td><td>{date(r.last_played_at)}</td>
        </tr>
      {:else}
        <tr><td colspan="6" class="muted">Noch keine Spieler erfasst.</td></tr>
      {/each}
    </tbody>
  </table>
</div>
{#if anyOpen}
  <p class="muted">
    Bei „läuft“ ist ein Match noch nicht beendet. Average, Siege und 180er erscheinen erst, wenn ein
    Leg abgeschlossen ist, in der Rangliste erst nach dem Matchende.
  </p>
{/if}
