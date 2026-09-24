<script lang="ts">
  import { goto } from '$app/navigation';
  import { api, type TournamentInput, type TournamentSummary } from '$lib/api';
  import TournamentForm from '$lib/components/TournamentForm.svelte';
  import { date } from '$lib/format';
  import { statusText } from '$lib/tournament';

  let list: TournamentSummary[] = $state([]);
  let loaded = $state(false);
  let admin = $state(false);
  let creating = $state(false);
  let error = $state('');

  api.get<TournamentSummary[]>('/api/tournaments').then((l) => { list = l; loaded = true; }).catch((e) => (error = e.message));
  api.get<{ admin: boolean }>('/api/admin/me').then((m) => (admin = m.admin)).catch(() => {});

  async function create(input: TournamentInput) {
    error = '';
    try {
      const r = await api.post<{ id: number }>('/api/admin/tournaments', input);
      await goto(`/turniere/${r.id}`);
    } catch (e) {
      error = (e as Error).message;
    }
  }
</script>

<h1>Turniere</h1>
{#if error}<p class="error">{error}</p>{/if}

{#if admin}
  {#if creating}
    <TournamentForm submitLabel="Turnier anlegen" onsave={create} />
  {:else}
    <p><button onclick={() => (creating = true)}>Neues Turnier</button></p>
  {/if}
{/if}

{#if !loaded && !error}
  <p class="muted">Lade …</p>
{:else if list.length === 0}
  <p class="muted">Noch keine Turniere.{#if !admin} Turniere legt der Admin an.{/if}</p>
{:else}
  <div class="card table-wrap">
    <table>
      <thead><tr><th>Turnier</th><th style="text-align:left">Status</th><th>Spieler</th><th>Datum</th><th style="text-align:left">Sieger</th></tr></thead>
      <tbody>
        {#each list as t (t.id)}
          <tr>
            <td><a href="/turniere/{t.id}">{t.name}</a></td>
            <td style="text-align:left">{#if t.status === 'running'}<span class="pill live">läuft</span>{:else}{statusText[t.status]}{/if}</td>
            <td>{t.players}</td>
            <td>{date(t.started_at ?? t.created_at)}</td>
            <td style="text-align:left">{#if t.champion}🏆 <a href="/spieler/{t.champion.player_id}">{t.champion.display_name}</a>{:else}–{/if}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}

<style>
  .live { color: var(--accent); border-color: color-mix(in srgb, var(--accent) 45%, transparent); }
</style>
