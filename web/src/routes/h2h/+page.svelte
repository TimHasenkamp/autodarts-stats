<script lang="ts">
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { api, qs, type Filter, type H2H, type Meta, type PlayerRow } from '$lib/api';
  import FilterBar from '$lib/components/FilterBar.svelte';
  import MatchList from '$lib/components/MatchList.svelte';
  import { num } from '$lib/format';

  let players: PlayerRow[] = $state([]);
  let meta: Meta | null = $state(null);
  let a = $state(Number(page.url.searchParams.get('a') || 0));
  let b = $state(Number(page.url.searchParams.get('b') || 0));
  let filter: Filter = $state({ from: '', to: '', variant: '' });
  let result: H2H | null = $state(null);
  let error = $state('');

  api.get<PlayerRow[]>('/api/players').then((r) => (players = r));
  api.get<Meta>('/api/meta').then((m) => (meta = m)).catch(() => {});

  $effect(() => {
    if (!a || !b || a === b) {
      result = null;
      return;
    }
    goto(`/h2h?a=${a}&b=${b}`, { replaceState: true, keepFocus: true, noScroll: true });
    api.get<H2H>('/api/h2h' + qs({ a, b, from: filter.from, to: filter.to, variant: filter.variant }))
      .then((r) => { result = r; error = ''; })
      .catch((e) => (error = e.message));
  });
</script>

<h1>Head-to-Head</h1>
<div class="card filters">
  <label>
    Spieler A
    <select bind:value={a}>
      <option value={0}>– wählen –</option>
      {#each players as p}<option value={p.player_id}>{p.display_name}</option>{/each}
    </select>
  </label>
  <label>
    Spieler B
    <select bind:value={b}>
      <option value={0}>– wählen –</option>
      {#each players as p}<option value={p.player_id}>{p.display_name}</option>{/each}
    </select>
  </label>
</div>
<FilterBar bind:filter variants={meta?.variants ?? []} />

{#if error}<p class="error">{error}</p>{/if}
{#if result}
  <div class="card">
    <div class="h2h">
      <div class="side">
        <a href="/spieler/{result.a.player_id}" class="name">{result.a.display_name}</a>
        <div class="big" class:win={result.wins_a > result.wins_b}>{result.wins_a}</div>
        <div class="muted">Legs {result.legs_a}</div>
        <div class="muted">Ø {num(result.average_a)}</div>
      </div>
      <div class="mid">
        <div class="muted">{result.matches} Matches</div>
        <div class="vs">:</div>
      </div>
      <div class="side">
        <a href="/spieler/{result.b.player_id}" class="name">{result.b.display_name}</a>
        <div class="big" class:win={result.wins_b > result.wins_a}>{result.wins_b}</div>
        <div class="muted">Legs {result.legs_b}</div>
        <div class="muted">Ø {num(result.average_b)}</div>
      </div>
    </div>
  </div>
  <h2>Gemeinsame Matches</h2>
  <div class="card"><MatchList matches={result.recent_matches} /></div>
{/if}

<style>
  .h2h { display: grid; grid-template-columns: 1fr auto 1fr; align-items: center; text-align: center; gap: 8px; }
  .name { font-weight: 600; font-size: 1.05rem; }
  .big { font-size: 2.4rem; font-weight: 700; }
  .vs { font-size: 2rem; color: var(--muted); }
</style>
