<script lang="ts">
  import type { Snippet } from 'svelte';

  // Skaliert den Inhalt so, dass er ohne Scrollen in die verfügbare Fläche
  // passt (nur verkleinern, bis maxScale auch vergrößern).
  let { children, maxScale = 1.6 }: { children: Snippet; maxScale?: number } = $props();

  let stage: HTMLDivElement;
  let inner: HTMLDivElement;
  let scale = $state(1);

  $effect(() => {
    const fit = () => {
      const w = inner.scrollWidth;
      const h = inner.scrollHeight;
      if (!w || !h) return;
      scale = Math.min(maxScale, stage.clientWidth / w, stage.clientHeight / h);
    };
    const ro = new ResizeObserver(fit);
    ro.observe(stage);
    ro.observe(inner);
    fit();
    return () => ro.disconnect();
  });
</script>

<div class="stage" bind:this={stage}>
  <div class="inner" bind:this={inner} style:transform="scale({scale})">
    {@render children()}
  </div>
</div>

<style>
  .stage { position: relative; width: 100%; height: 100%; overflow: hidden; display: flex; justify-content: center; align-items: center; }
  .inner { width: max-content; max-width: none; transform-origin: center center; flex: none; }
</style>
