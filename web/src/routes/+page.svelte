<script lang="ts">
  import { api, qs, type Filter, type Meta, type PlayerRow } from '$lib/api';
  import FilterBar from '$lib/components/FilterBar.svelte';
  import { num, pct } from '$lib/format';

  let filter: Filter = $state({ from: '', to: '', variant: '', min_matches: 5, sort: 'average', order: 'desc' });
  let rows: PlayerRow[] = $state([]);
  let meta: Meta | null = $state(null);
  let error = $state('');
  let loading = $state(true);

  const cols: { key: string; label: string; render: (r: PlayerRow) => string }[] = [
    { key: 'matches', label: 'Sp', render: (r) => String(r.matches) },
    { key: 'wins', label: 'S', render: (r) => String(r.wins) },
    { key: 'win_rate', label: 'Quote', render: (r) => pct(r.win_rate) },
    { key: 'average', label: 'Avg', render: (r) => num(r.average) },
    { key: 'first9', label: 'First 9', render: (r) => num(r.first9_avg) },
    { key: 'checkout', label: 'CO %', render: (r) => pct(r.checkout_rate) },
    { key: 'highest_checkout', label: 'High CO', render: (r) => (r.highest_checkout ? String(r.highest_checkout) : '–') },
    { key: 'count_180', label: '180', render: (r) => String(r.count_180) },
    { key: 'legs', label: 'Legs', render: (r) => `${r.legs_won}/${r.legs_played}` },
  ];

  async function load() {
    loading = true;
    error = '';
    try {
      rows = await api.get<PlayerRow[]>('/api/leaderboard' + qs({ ...filter }));
    } catch (e) {
      error = (e as Error).message;
    } finally {
      loading = false;
    }
  }
  $effect(() => {
    // Abhaengigkeiten lesen
    void filter.from; void filter.to; void filter.variant; void filter.min_matches; void filter.sort; void filter.order;
    load();
  });
  api.get<Meta>('/api/meta').then((m) => (meta = m)).catch(() => {});

  function sortBy(key: string) {
    if (key === 'legs') return;
    if (filter.sort === key) filter.order = filter.order === 'desc' ? 'asc' : 'desc';
    else {
      filter.sort = key;
      filter.order = key === 'name' ? 'asc' : 'desc';
    }
  }
</script>

<h1>Rangliste</h1>
{#if meta}
  <p class="muted">{meta.players} Spieler · {meta.matches} beendete Matches · {meta.legs} Legs</p>
{/if}
<FilterBar bind:filter variants={meta?.variants ?? []} showMinMatches />

{#if error}
  <p class="error">{error}</p>
{:else if loading && rows.length === 0}
  <p class="muted">Lade …</p>
{:else if rows.length === 0}
  <p class="muted">Keine Spieler mit mindestens {filter.min_matches} Matches im Zeitraum.</p>
{:else}
  <div class="card table-wrap">
    <table>
      <thead>
        <tr>
          <th>#</th>
          <th class="sortable" class:active={filter.sort === 'name'} onclick={() => sortBy('name')} style="text-align:left">Spieler</th>
          {#each cols as c}
            <th class="sortable" class:active={filter.sort === c.key} onclick={() => sortBy(c.key)}>
              {c.label}{#if filter.sort === c.key}{filter.order === 'desc' ? ' ▼' : ' ▲'}{/if}
            </th>
          {/each}
        </tr>
      </thead>
      <tbody>
        {#each rows as r, i}
          <tr>
            <td class="muted">{i + 1}</td>
            <td style="text-align:left"><a href="/spieler/{r.player_id}">{r.display_name}</a></td>
            {#each cols as c}
              <td>{c.render(r)}</td>
            {/each}
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  <p class="muted">Sp = Matches, S = Siege. Average ist dartgewichtet über alle erfassten Legs (auch aus abgebrochenen Matches).</p>
{/if}
