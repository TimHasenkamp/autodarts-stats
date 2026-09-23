<script lang="ts">
  import '../app.css';
  import { page } from '$app/state';

  let { children } = $props();
  const links = [
    { href: '/', label: 'Rangliste' },
    { href: '/spieler', label: 'Spieler' },
    { href: '/h2h', label: 'Head-to-Head' },
    { href: '/matches', label: 'Matches' },
    { href: '/admin', label: 'Admin' },
  ];
  function isActive(href: string): boolean {
    const p = page.url.pathname;
    return href === '/' ? p === '/' : p.startsWith(href);
  }
</script>

<nav class="top">
  <a href="/" class="brand"><img src="/favicon.svg" alt="" width="22" height="22" /> Your Darts</a>
  {#each links as l}
    <a href={l.href} class:active={isActive(l.href)}>{l.label}</a>
  {/each}
</nav>
<main class="container">
  {@render children()}
  <p class="muted" style="margin-top: 32px; font-size: 0.8rem">
    Es werden Spielernamen und Spielstatistiken gespeichert. <a href="/datenschutz">Datenschutz</a>
  </p>
</main>
