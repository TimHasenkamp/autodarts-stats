<script lang="ts">
  import type { HistoryPoint } from '$lib/api';
  import { date, num } from '$lib/format';

  let { points }: { points: HistoryPoint[] } = $props();

  const W = 640;
  const H = 220;
  const PAD = { l: 36, r: 10, t: 10, b: 24 };

  let data = $derived(points.filter((p) => p.average !== null) as (HistoryPoint & { average: number })[]);
  let min = $derived(data.length ? Math.floor(Math.min(...data.map((p) => p.average)) / 10) * 10 : 0);
  let max = $derived(data.length ? Math.ceil(Math.max(...data.map((p) => p.average)) / 10) * 10 : 100);
  let span = $derived(Math.max(max - min, 10));

  function x(i: number): number {
    if (data.length <= 1) return PAD.l + (W - PAD.l - PAD.r) / 2;
    return PAD.l + (i / (data.length - 1)) * (W - PAD.l - PAD.r);
  }
  function y(v: number): number {
    return PAD.t + (1 - (v - min) / span) * (H - PAD.t - PAD.b);
  }
  let path = $derived(data.map((p, i) => `${i === 0 ? 'M' : 'L'}${x(i).toFixed(1)},${y(p.average).toFixed(1)}`).join(' '));
  let area = $derived(data.length ? `${path} L${x(data.length - 1).toFixed(1)},${H - PAD.b} L${x(0).toFixed(1)},${H - PAD.b} Z` : '');
  let ticks = $derived([0, 0.25, 0.5, 0.75, 1].map((f) => min + f * span));
  let hover: number | null = $state(null);

  function onMove(e: MouseEvent | TouchEvent) {
    const svg = e.currentTarget as SVGSVGElement;
    const rect = svg.getBoundingClientRect();
    const cx = 'touches' in e ? e.touches[0].clientX : e.clientX;
    const px = ((cx - rect.left) / rect.width) * W;
    if (data.length === 0) return;
    let best = 0;
    for (let i = 1; i < data.length; i++) if (Math.abs(x(i) - px) < Math.abs(x(best) - px)) best = i;
    hover = best;
  }
</script>

{#if data.length === 0}
  <p class="muted">Noch keine Averages.</p>
{:else}
  <svg viewBox="0 0 {W} {H}" role="img" aria-label="Average-Verlauf" style="width:100%;height:auto;display:block"
    onmousemove={onMove} ontouchmove={onMove} onmouseleave={() => (hover = null)}>
    {#each ticks as t}
      <line x1={PAD.l} x2={W - PAD.r} y1={y(t)} y2={y(t)} stroke="var(--border)" stroke-width="1" />
      <text x={PAD.l - 6} y={y(t) + 4} text-anchor="end" font-size="11" fill="var(--muted)">{Math.round(t)}</text>
    {/each}
    <path d={area} fill="var(--chart-bg)" />
    <path d={path} fill="none" stroke="var(--chart)" stroke-width="2" stroke-linejoin="round" />
    {#each data as p, i}
      <circle cx={x(i)} cy={y(p.average)} r={hover === i ? 5 : 3} fill={p.won ? 'var(--win)' : 'var(--loss)'} />
    {/each}
    {#if hover !== null}
      {@const p = data[hover]}
      {@const tx = Math.min(Math.max(x(hover), PAD.l + 60), W - PAD.r - 60)}
      <line x1={x(hover)} x2={x(hover)} y1={PAD.t} y2={H - PAD.b} stroke="var(--muted)" stroke-dasharray="3 3" />
      <rect x={tx - 60} y={PAD.t} width="120" height="34" rx="6" fill="var(--card)" stroke="var(--border)" />
      <text x={tx} y={PAD.t + 14} text-anchor="middle" font-size="11" fill="var(--muted)">{date(p.played_at)} · {p.won ? 'Sieg' : 'Niederlage'}</text>
      <text x={tx} y={PAD.t + 28} text-anchor="middle" font-size="13" font-weight="700" fill="var(--text)">Ø {num(p.average)}</text>
    {/if}
    <text x={PAD.l} y={H - 6} font-size="11" fill="var(--muted)">{date(data[0].played_at)}</text>
    <text x={W - PAD.r} y={H - 6} text-anchor="end" font-size="11" fill="var(--muted)">{date(data[data.length - 1].played_at)}</text>
  </svg>
{/if}
