<script lang="ts">
  import type { Filter } from '$lib/api';
  import { isoDay } from '$lib/format';

  let {
    filter = $bindable(),
    variants = [],
    showMinMatches = false,
  }: { filter: Filter; variants?: string[]; showMinMatches?: boolean } = $props();

  function preset(days: number | null) {
    if (days === null) {
      filter = { ...filter, from: '', to: '' };
      return;
    }
    const to = new Date();
    const from = new Date(to.getTime() - days * 86400000);
    filter = { ...filter, from: isoDay(from), to: isoDay(to) };
  }
</script>

<div class="card filters">
  <label>
    Von
    <input type="date" bind:value={filter.from} />
  </label>
  <label>
    Bis
    <input type="date" bind:value={filter.to} />
  </label>
  <label>
    Variante
    <select bind:value={filter.variant}>
      <option value="">Alle</option>
      {#each variants as v}
        <option value={v}>{v}</option>
      {/each}
    </select>
  </label>
  {#if showMinMatches}
    <label>
      Min. Matches
      <input type="number" min="0" step="1" bind:value={filter.min_matches} style="width: 90px" />
    </label>
  {/if}
  <div class="row">
    <button class="secondary" onclick={() => preset(7)}>7 Tage</button>
    <button class="secondary" onclick={() => preset(30)}>30 Tage</button>
    <button class="secondary" onclick={() => preset(365)}>1 Jahr</button>
    <button class="secondary" onclick={() => preset(null)}>Alle</button>
  </div>
</div>
