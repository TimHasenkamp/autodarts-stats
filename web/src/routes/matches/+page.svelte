<script lang="ts">
  import { api, type MatchSummary } from '$lib/api';
  import MatchList from '$lib/components/MatchList.svelte';

  let matches: MatchSummary[] = $state([]);
  let error = $state('');
  api.get<MatchSummary[]>('/api/matches?limit=100').then((m) => (matches = m)).catch((e) => (error = e.message));
</script>

<h1>Letzte Matches</h1>
{#if error}<p class="error">{error}</p>{/if}
<div class="card"><MatchList {matches} /></div>
