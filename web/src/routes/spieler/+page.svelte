<script lang="ts">
  import { api, type PlayerRow } from '$lib/api';
  import { date, num } from '$lib/format';

  let rows: PlayerRow[] = $state([]);
  let q = $state('');
  let error = $state('');
  api.get<PlayerRow[]>('/api/players').then((r) => (rows = r)).catch((e) => (error = e.message));
  let filtered = $derived(rows.filter((r) => r.display_name.toLowerCase().includes(q.toLowerCase())));
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
          <td><a href="/spieler/{r.player_id}">{r.display_name}</a></td>
          <td>{r.matches}</td><td>{r.wins}</td><td>{num(r.average)}</td><td>{r.count_180}</td><td>{date(r.last_played_at)}</td>
        </tr>
      {:else}
        <tr><td colspan="6" class="muted">Keine Spieler.</td></tr>
      {/each}
    </tbody>
  </table>
</div>
