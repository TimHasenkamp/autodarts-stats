<script lang="ts">
  import type { MatchSummary } from '$lib/api';
  import { dateTime, num } from '$lib/format';

  let { matches, highlight = 0 }: { matches: MatchSummary[]; highlight?: number } = $props();
</script>

{#if matches.length === 0}
  <p class="muted">Keine Matches.</p>
{:else}
  <div class="table-wrap">
    <table>
      <thead>
        <tr>
          <th>Datum</th>
          <th style="text-align:left">Variante</th>
          <th style="text-align:left">Spieler</th>
          <th>Legs</th>
          <th>Avg</th>
        </tr>
      </thead>
      <tbody>
        {#each matches as m}
          <tr>
            <td><a href="/match/{m.match_id}">{dateTime(m.played_at)}</a>{#if !m.finished} <span class="pill">läuft</span>{/if}</td>
            <td style="text-align:left">{m.variant || '–'}</td>
            <td style="text-align:left">
              {#each m.players as p, i}
                {#if i > 0}<span class="muted"> vs </span>{/if}
                <a href="/spieler/{p.player_id}" class:win={p.won} style:font-weight={p.player_id === highlight ? 700 : undefined}>{p.display_name}</a>
              {/each}
            </td>
            <td>{m.players.map((p) => p.legs_won).join(':')}</td>
            <td>{m.players.map((p) => num(p.average)).join(' / ')}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}
