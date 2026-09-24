<script lang="ts">
  import { page } from '$app/state';
  import { api, qs, type Filter, type Meta, type PlayerTournaments, type Profile } from '$lib/api';
  import AverageChart from '$lib/components/AverageChart.svelte';
  import FilterBar from '$lib/components/FilterBar.svelte';
  import MatchList from '$lib/components/MatchList.svelte';
  import { date, num, pct } from '$lib/format';

  let filter: Filter = $state({ from: '', to: '', variant: '' });
  let profile: Profile | null = $state(null);
  let meta: Meta | null = $state(null);
  let error = $state('');
  let tournaments: PlayerTournaments | null = $state(null);
  api.get<Meta>('/api/meta').then((m) => (meta = m)).catch(() => {});

  $effect(() => {
    const id = page.params.id;
    const q = qs({ from: filter.from, to: filter.to, variant: filter.variant });
    api.get<Profile>(`/api/players/${id}${q}`).then((p) => { profile = p; error = ''; }).catch((e) => (error = e.message));
  });
  $effect(() => {
    api.get<PlayerTournaments>(`/api/players/${page.params.id}/tournaments`).then((t) => (tournaments = t)).catch(() => (tournaments = null));
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
  {#if p.open_matches > 0}
    <p class="muted">
      {p.open_matches === 1 ? 'Ein Match läuft noch' : `${p.open_matches} Matches laufen noch`} und
      zählt hier erst nach dem Ende.
    </p>
  {/if}
  {#if p.matches === 0 && p.legs_played === 0}
    <p class="muted">Noch keine abgeschlossenen Legs. Die Zahlen bleiben leer, bis ein Leg fertig ist.</p>
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

  {#if tournaments && tournaments.tournaments.length}
    <h2>Turniere</h2>
    <div class="card">
      <p style="margin-top: 0">
        {tournaments.tournaments.length} Teilnahme{tournaments.tournaments.length === 1 ? '' : 'n'}
        · {tournaments.wins} Turniersieg{tournaments.wins === 1 ? '' : 'e'}
        · {tournaments.podium}× auf dem Podest
      </p>
      <div class="table-wrap">
        <table>
          <thead><tr><th>Turnier</th><th>Datum</th><th>Platz</th><th style="text-align:left">Erreicht</th><th>Spiele</th></tr></thead>
          <tbody>
            {#each tournaments.tournaments as t (t.tournament_id)}
              <tr>
                <td><a href="/turniere/{t.tournament_id}">{t.name}</a>{#if t.status === 'running'} <span class="pill">läuft</span>{/if}</td>
                <td>{date(t.started_at)}</td>
                <td>{#if t.place === 1}🏆 1{:else}{t.place || '–'}{/if} <span class="muted">/ {t.players}</span></td>
                <td style="text-align:left">{t.label}{#if t.lucky_loser} <span class="pill" title="hat in der Lucky-Loser-Runde gespielt">LL</span>{/if}</td>
                <td>{t.wins}/{t.matches}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>
  {/if}

  <h2>Letzte Matches</h2>
  <div class="card"><MatchList matches={profile.recent_matches} highlight={p.player_id} /></div>
  <p><a href="/h2h?a={p.player_id}">Head-to-Head mit {p.display_name} …</a></p>
{/if}
