<script lang="ts">
  import { page } from '$app/state';
  import { api, qs, type Filter, type Meta, type Profile } from '$lib/api';
  import AverageChart from '$lib/components/AverageChart.svelte';
  import FilterBar from '$lib/components/FilterBar.svelte';
  import MatchList from '$lib/components/MatchList.svelte';
  import { num, pct } from '$lib/format';

  let filter: Filter = $state({ from: '', to: '', variant: '' });
  let profile: Profile | null = $state(null);
  let meta: Meta | null = $state(null);
  let error = $state('');
  api.get<Meta>('/api/meta').then((m) => (meta = m)).catch(() => {});

  $effect(() => {
    const id = page.params.id;
    const q = qs({ from: filter.from, to: filter.to, variant: filter.variant });
    api.get<Profile>(`/api/players/${id}${q}`).then((p) => { profile = p; error = ''; }).catch((e) => (error = e.message));
  });
</script>

{#if error}
  <p class="error">{error}</p>
{:else if !profile}
  <p class="muted">Lade …</p>
{:else}
  {@const p = profile.player}
  <h1>{p.display_name} {#if profile.has_account}<span class="pill" title="Autodarts-Account">Account</span>{/if}</h1>
  {#if profile.aliases.length > 1}
    <p class="muted">Auch als: {profile.aliases.join(', ')}</p>
  {/if}
  <FilterBar bind:filter variants={meta?.variants ?? []} />

  <div class="card stats-grid">
    <div class="stat"><div class="label">Matches</div><div class="value">{p.matches}</div></div>
    <div class="stat"><div class="label">Siege</div><div class="value">{p.wins} <span class="muted" style="font-size:0.9rem">({pct(p.win_rate)})</span></div></div>
    <div class="stat"><div class="label">Average</div><div class="value">{num(p.average)}</div></div>
    <div class="stat"><div class="label">First 9</div><div class="value">{num(p.first9_avg)}</div></div>
    <div class="stat"><div class="label">Checkout</div><div class="value">{pct(p.checkout_rate)}</div></div>
    <div class="stat"><div class="label">Legs</div><div class="value">{p.legs_won}/{p.legs_played}</div></div>
    <div class="stat"><div class="label">180er</div><div class="value">{p.count_180}</div></div>
    <div class="stat"><div class="label">140+</div><div class="value">{p.count_140plus}</div></div>
    <div class="stat"><div class="label">100+</div><div class="value">{p.count_100plus}</div></div>
  </div>

  <h2>Bestwerte</h2>
  <div class="card stats-grid">
    <div class="stat"><div class="label">Bester Match-Avg</div><div class="value">{num(p.best_match_average)}</div></div>
    <div class="stat"><div class="label">Bester Leg-Avg</div><div class="value">{num(profile.best_leg_average)}</div></div>
    <div class="stat"><div class="label">Höchstes Finish</div><div class="value">{p.highest_checkout || '–'}</div></div>
    <div class="stat"><div class="label">Kürzestes Leg</div><div class="value">{profile.best_leg_darts ? profile.best_leg_darts + ' Darts' : '–'}</div></div>
    <div class="stat"><div class="label">Meiste 180 / Match</div><div class="value">{profile.most_180_match}</div></div>
  </div>

  <h2>Average-Verlauf</h2>
  <div class="card"><AverageChart points={profile.history} /></div>

  {#if profile.variants.length > 1}
    <h2>Varianten</h2>
    <div class="card row">
      {#each profile.variants as v}
        <span class="pill">{v.variant}: {v.wins}/{v.matches} Siege</span>
      {/each}
    </div>
  {/if}

  <h2>Letzte Matches</h2>
  <div class="card"><MatchList matches={profile.recent_matches} highlight={p.player_id} /></div>
  <p><a href="/h2h?a={p.player_id}">Head-to-Head mit {p.display_name} …</a></p>
{/if}
