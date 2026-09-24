<script lang="ts">
  import type { TMatch, TRound } from '$lib/api';
  import { ruleText } from '$lib/tournament';

  let {
    rounds,
    selected = '',
    onselect,
    big = false,
  }: { rounds: TRound[]; selected?: string; onselect?: (m: TMatch) => void; big?: boolean } = $props();

  function clickable(m: TMatch): boolean {
    return !!onselect && (m.status === 'ready' || m.status === 'done');
  }
</script>

<div class="bracket" class:big>
  {#each rounds as r}
    <div class="round">
      <div class="head">
        <strong>{r.name}</strong>
        <span class="muted">{ruleText(r.rule)}</span>
      </div>
      <div class="matches">
        {#each r.matches as m (m.key)}
          <button
            type="button"
            class="match {m.status}"
            class:sel={selected === m.key}
            disabled={!clickable(m)}
            onclick={() => onselect?.(m)}
            title={m.warning ? '⚠ ' + m.warning : undefined}
          >
            <span class="no">
              {m.no}
              {#if m.status === 'ready'}<span class="live">offen</span>{/if}
              {#if m.warning}<span class="warn">⚠</span>{/if}
              {#if m.source === 'manual'}<span class="src" title="von Hand eingetragen">✎</span>{/if}
            </span>
            {#each [m.a, m.b] as s}
              <span class="side" class:won={s.won} class:ph={!s.player_id}>
                <span class="name">{s.display_name ?? s.placeholder ?? ''}</span>
                <span class="legs">{s.legs ?? ''}</span>
              </span>
            {/each}
            {#if m.target}<span class="muted target">Sieger → {m.target}</span>{/if}
          </button>
        {/each}
      </div>
    </div>
  {/each}
</div>

<style>
  .bracket { display: flex; gap: 20px; overflow-x: auto; -webkit-overflow-scrolling: touch; padding-bottom: 6px; }
  .round { display: flex; flex-direction: column; min-width: 190px; max-width: 320px; flex: 1 0 190px; }
  .head { display: flex; flex-direction: column; margin-bottom: 10px; padding-bottom: 8px; border-bottom: 1px solid var(--border); }
  .head strong { font-family: var(--font-heading); font-size: 0.98rem; color: var(--heading); }
  .head .muted { font-size: 0.72rem; }
  .matches { flex: 1; display: flex; flex-direction: column; justify-content: space-around; gap: 10px; }
  .match {
    display: flex; flex-direction: column; gap: 3px; text-align: left; width: 100%;
    background: var(--card); color: var(--text); border: 1px solid var(--border); border-radius: var(--radius-sm);
    padding: 8px 12px; min-height: 0; cursor: default; font-size: 0.88rem; font-weight: 400; box-shadow: var(--shadow);
    transition: border-color 0.15s, background-color 0.15s;
  }
  .match:disabled { opacity: 1; }
  .match:not(:disabled) { cursor: pointer; }
  .match:not(:disabled):hover { border-color: var(--accent); background: var(--card); }
  .match.ready { border-color: color-mix(in srgb, var(--accent) 55%, transparent); background: var(--accent-soft); }
  .match.walkover, .match.empty { opacity: 0.45; box-shadow: none; background: transparent; }
  .match.sel { outline: 2px solid var(--accent); outline-offset: 2px; }
  .no { font-size: 0.68rem; color: var(--muted); display: flex; gap: 8px; align-items: center; letter-spacing: 0.04em; }
  .live { color: var(--accent); font-weight: 500; text-transform: uppercase; letter-spacing: 0.08em; font-size: 0.62rem; }
  .warn { color: #b7791f; }
  .side { display: flex; justify-content: space-between; gap: 8px; }
  .side .name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .side .legs { font-family: var(--font-heading); font-variant-numeric: tabular-nums; font-weight: 700; color: var(--muted); }
  .side.won { font-weight: 600; color: var(--heading); }
  .side.won .legs { color: var(--accent); }
  .side.ph { color: var(--muted); font-size: 0.95em; }
  .target { font-size: 0.72rem; }
  .big .round { min-width: 240px; max-width: 460px; flex-basis: 240px; }
  .big .head strong { font-size: 1.25rem; }
  .big .head .muted { font-size: 0.9rem; }
  .big .match { font-size: 1.25rem; padding: 12px 16px; }
  .big .no { font-size: 0.8rem; }
  .big .live { font-size: 0.75rem; }
</style>
