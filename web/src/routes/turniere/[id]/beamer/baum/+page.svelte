<script lang="ts">
  import { page } from '$app/state';
  import type { TRound, TournamentView } from '$lib/api';
  import Bracket from '$lib/components/Bracket.svelte';
  import FitToScreen from '$lib/components/FitToScreen.svelte';
  import { statusText } from '$lib/tournament';

  // Beamer-Liveansicht des kompletten Turnierbaums (Hauptrunde, Sieger,
  // Spiel um Platz 3, Lucky-Loser-Runde) auf einem Bildschirm, ohne Scrollen.
  // Nur öffentliche API, kein Login nötig. Aktualisierung per Polling.
  // URL-Parameter: ?intervall=15 (Sekunden, min. 5), ?hell (helles Farbschema).
  // Tasten: R neu laden, F Vollbild.

  const REQUEST_TIMEOUT = 10_000;

  let view: TournamentView | null = $state(null);
  let updatedAt: Date | null = $state(null);
  let failed = $state(false);
  let errorMsg = $state('');
  let notFound = $state(false);
  let loading = $state(false);
  let idle = $state(false);

  const id = $derived(page.params.id ?? '');
  const interval = $derived(Math.max(5, Number(page.url.searchParams.get('intervall')) || 15));
  const light = $derived(page.url.searchParams.has('hell'));

  const llRounds = $derived.by((): TRound[] => {
    if (!view) return [];
    const rs = [...view.lucky_rounds];
    if (view.playin) rs.push({ name: 'Zusatzspiel', rule: view.playin.rule, matches: [view.playin] });
    return rs;
  });
  const thirdRounds = $derived.by((): TRound[] =>
    view?.third ? [{ name: 'Spiel um Platz 3', rule: view.third.rule, matches: [view.third] }] : [],
  );

  const fmtTime = (d: Date) => d.toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit', second: '2-digit' });

  // --- Polling -------------------------------------------------------------
  // Höchstens ein Request gleichzeitig; der nächste wird erst nach dem Ende des
  // vorigen geplant (setTimeout-Kette statt setInterval). Bei unsichtbarem Tab
  // wird nichts geplant, beim Sichtbarwerden sofort geladen.
  let inflight: Promise<void> | null = null;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let ctrl: AbortController | null = null;
  let active = false;
  let lastRaw = '';

  function schedule() {
    clearTimeout(timer);
    if (!active || document.visibilityState === 'hidden') return;
    timer = setTimeout(refresh, interval * 1000);
  }

  // Erhöht sich bei jedem Neustart (Seitenwechsel/ID-Wechsel); Antworten
  // älterer Durchläufe werden verworfen.
  let gen = 0;

  function refresh(): Promise<void> {
    if (inflight) return inflight;
    clearTimeout(timer);
    loading = true;
    const myGen = gen;
    const c = new AbortController();
    ctrl = c;
    const to = setTimeout(() => c.abort(), REQUEST_TIMEOUT);
    const url = `/api/tournaments/${encodeURIComponent(id)}`;
    let p: Promise<void> | null = null;
    p = (async () => {
      try {
        const res = await fetch(url, { cache: 'no-store', signal: c.signal, headers: { Accept: 'application/json' } });
        const text = await res.text();
        if (!res.ok) {
          let msg = res.statusText || `HTTP ${res.status}`;
          try {
            const d = JSON.parse(text);
            if (d && typeof d === 'object' && 'error' in d) msg = String(d.error);
          } catch {
            /* kein JSON */
          }
          if (myGen === gen) notFound = res.status === 404;
          throw new Error(msg);
        }
        if (myGen !== gen) return;
        // Unveränderte Antwort nicht neu zuweisen – vermeidet jedes Neuzeichnen.
        if (text !== lastRaw || !view) {
          view = JSON.parse(text) as TournamentView;
          lastRaw = text;
        }
        notFound = false;
        failed = false;
        errorMsg = '';
        updatedAt = new Date();
      } catch (e) {
        if (myGen !== gen) return;
        failed = true;
        errorMsg = (e as Error).name === 'AbortError' ? 'Zeitüberschreitung' : (e as Error).message;
      } finally {
        clearTimeout(to);
        if (ctrl === c) ctrl = null;
        if (inflight === p) {
          inflight = null;
          loading = false;
          schedule();
        }
      }
    })();
    inflight = p;
    return p;
  }

  $effect(() => {
    void id;
    active = true;
    view = null;
    lastRaw = '';
    updatedAt = null;
    failed = false;
    notFound = false;
    refresh();

    const onVisibility = () => {
      if (document.visibilityState === 'hidden') clearTimeout(timer);
      else refresh();
    };
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      active = false;
      gen++;
      clearTimeout(timer);
      ctrl?.abort();
      ctrl = null;
      inflight = null;
      loading = false;
      document.removeEventListener('visibilitychange', onVisibility);
    };
  });

  // --- Bedienung -----------------------------------------------------------
  let idleTimer: ReturnType<typeof setTimeout> | undefined;
  function wake() {
    idle = false;
    clearTimeout(idleTimer);
    idleTimer = setTimeout(() => (idle = true), 3000);
  }
  $effect(() => () => clearTimeout(idleTimer));

  function fullscreen() {
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
    else document.documentElement.requestFullscreen?.().catch(() => {});
  }
  function key(e: KeyboardEvent) {
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    if (e.key === 'r' || e.key === 'R') refresh();
    else if (e.key === 'f' || e.key === 'F') fullscreen();
    else return;
    wake();
  }
</script>

<svelte:head><title>Turnierbaum · {view?.name ?? 'Turnier'} · Your Darts</title></svelte:head>
<svelte:window onkeydown={key} onmousemove={wake} />

<div class="tv-root" class:light class:idle>
  <header>
    <div class="brand">
      <img src="/favicon.svg" alt="" width="40" height="40" />
      <span class="name">{view?.name ?? 'Turnier'}</span>
      {#if view}<span class="state {view.status}">{statusText[view.status] ?? view.status}</span>{/if}
    </div>
    <div class="right">
      {#if failed && view}
        <span class="offline" role="status" data-testid="bracket-offline">Verbindung verloren</span>
      {/if}
      <span class="updated" data-testid="bracket-updated" aria-live="polite">Stand: {updatedAt ? fmtTime(updatedAt) : '–'}</span>
      <button
        type="button"
        class="ctl"
        class:spinning={loading}
        data-testid="bracket-reload"
        onclick={() => refresh()}
        title="Jetzt neu laden (R)"
        aria-label="Jetzt neu laden"
        aria-busy={loading}
      >
        <span class="ico" aria-hidden="true">↻</span> Neu laden
      </button>
      <button type="button" class="ctl" onclick={fullscreen} title="Vollbild (F)" aria-label="Vollbild">⛶</button>
    </div>
  </header>

  <main>
    {#if !view}
      <p class="msg">
        {#if notFound}Turnier nicht gefunden.
        {:else if failed}Keine Verbindung zum Server ({errorMsg}). Neuer Versuch in {interval} s …
        {:else}Lade …{/if}
      </p>
    {:else if view.status === 'draft'}
      <p class="msg">Das Turnier „{view.name}“ ist noch nicht gestartet.</p>
    {:else}
      {@const v = view}
      <FitToScreen maxScale={2.2}>
        <div class="tree" data-testid="bracket">
          <div class="mainrow">
            <Bracket rounds={v.main} tv />
            <div class="champ-col">
              <div class="champ-head"><strong>Sieger</strong><span>&nbsp;</span></div>
              <div class="champ-body">
                {#if v.champion}
                  <div class="champ won">
                    <span class="trophy" aria-hidden="true">🏆</span>
                    <span class="champ-name" data-testid="tournament-champion">{v.champion.display_name}</span>
                  </div>
                {:else}
                  <div class="champ open">
                    <span class="trophy" aria-hidden="true">🏆</span>
                    <span class="champ-name">?</span>
                  </div>
                {/if}
              </div>
            </div>
          </div>

          {#if llRounds.length || thirdRounds.length}
            <div class="extras">
              {#if llRounds.length}
                <section class="extra">
                  <h2>Lucky-Loser-Runde</h2>
                  {#if v.ll_entry_name}<p class="hint">Der Sieger zieht über das Zusatzspiel ins {v.ll_entry_name} ein.</p>{/if}
                  <Bracket rounds={llRounds} tv />
                </section>
              {/if}
              {#if thirdRounds.length}
                <section class="extra">
                  <h2>Platz 3</h2>
                  <Bracket rounds={thirdRounds} tv />
                </section>
              {/if}
            </div>
          {/if}
        </div>
      </FitToScreen>
    {/if}
  </main>
</div>

<style>
  /* Eigenes, kontraststarkes Farbschema (Standard dunkel, ?hell für hell). Die
     Variablen gelten auch für den eingebetteten Bracket. */
  .tv-root {
    --bg: #040b14;
    --card: #0b1a2b;
    --text: #f3f6fa;
    --muted: #a3b1c2;
    --border: rgba(255, 255, 255, 0.2);
    --border-strong: rgba(255, 255, 255, 0.38);
    --accent: #3fd6cf;
    --accent-soft: rgba(63, 214, 207, 0.16);
    --heading: #ffffff;
    --gold: #ffd166;
    position: fixed; inset: 0; display: flex; flex-direction: column; overflow: hidden;
    background: var(--bg); color: var(--text);
  }
  .tv-root.light {
    --bg: #ffffff;
    --card: #ffffff;
    --text: #00101f;
    --muted: #45505e;
    --border: #b8c1cc;
    --border-strong: #7d8896;
    --accent: #006d6f;
    --accent-soft: rgba(0, 153, 154, 0.12);
    --heading: #00264d;
    --gold: #9a6700;
  }
  .tv-root.idle, .tv-root.idle * { cursor: none; }

  header {
    position: relative; flex: none; display: flex; align-items: center; justify-content: space-between; gap: 24px;
    padding: 14px 32px; background: var(--navy); color: #fff;
    background-image: radial-gradient(80% 260% at 100% 0%, rgba(0, 153, 154, 0.35) 0%, rgba(0, 153, 154, 0) 60%);
  }
  header::after { content: ''; position: absolute; left: 0; right: 0; bottom: 0; height: 3px; background: var(--brand-line); }
  .brand { display: flex; align-items: center; gap: 16px; min-width: 0; font-family: var(--font-heading); font-weight: 700; font-size: 2rem; }
  .brand img { filter: brightness(0) invert(1); flex: none; }
  .brand .name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .state {
    flex: none; font-family: var(--font-text); font-size: 0.95rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.08em;
    padding: 3px 12px; border-radius: 999px; border: 1px solid rgba(255, 255, 255, 0.35); color: #cfeeea;
  }
  .state.running { border-color: #3fd6cf; color: #3fd6cf; }
  .right { display: flex; align-items: center; gap: 18px; flex: none; }
  .updated { font-family: var(--font-heading); font-size: 1.5rem; font-variant-numeric: tabular-nums; white-space: nowrap; }
  .offline {
    font-size: 1rem; font-weight: 600; color: #ffd0d9; background: rgba(200, 37, 79, 0.35);
    border: 1px solid rgba(255, 122, 149, 0.6); border-radius: 999px; padding: 3px 14px; white-space: nowrap;
  }
  .ctl {
    background: rgba(255, 255, 255, 0.1); color: #fff; border: 1px solid rgba(255, 255, 255, 0.3);
    padding: 6px 16px; font-size: 1.1rem; display: inline-flex; align-items: center; gap: 8px;
  }
  .ctl:hover:not(:disabled) { background: rgba(255, 255, 255, 0.2); }
  .ctl .ico { display: inline-block; font-size: 1.25rem; line-height: 1; }
  .ctl.spinning .ico { animation: spin 0.9s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  @media (prefers-reduced-motion: reduce) { .ctl.spinning .ico { animation: none; opacity: 0.5; } }

  main { position: relative; flex: 1; min-height: 0; padding: 28px 36px; }
  .msg { display: grid; place-items: center; height: 100%; margin: 0; font-size: 2rem; color: var(--muted); text-align: center; }

  .tree { display: flex; flex-direction: column; gap: 40px; color: var(--text); }
  .mainrow { display: flex; gap: 34px; align-items: stretch; }
  .champ-col { width: 330px; flex: none; display: flex; flex-direction: column; }
  .champ-head {
    display: flex; flex-direction: column; margin-bottom: 16px; padding-bottom: 8px; border-bottom: 2px solid var(--border-strong);
  }
  .champ-head strong { font-family: var(--font-heading); font-size: 1.6rem; color: var(--heading); }
  .champ-head span { font-size: 0.95rem; }
  .champ-body { flex: 1; display: flex; flex-direction: column; justify-content: center; }
  .champ {
    display: flex; flex-direction: column; align-items: center; gap: 6px; padding: 20px 18px; text-align: center;
    border: 3px solid var(--border-strong); border-radius: var(--radius); background: var(--card);
  }
  .champ.won { border-color: var(--gold); box-shadow: 0 0 0 4px color-mix(in srgb, var(--gold) 25%, transparent); }
  .champ.open { opacity: 0.55; border-style: dashed; }
  .trophy { font-size: 3rem; line-height: 1; }
  .champ-name {
    font-family: var(--font-heading); font-weight: 700; font-size: 2.1rem; color: var(--heading);
    max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .champ.won .champ-name { color: var(--gold); }

  .extras { display: flex; gap: 60px; align-items: flex-start; padding-top: 28px; border-top: 2px solid var(--border); }
  .extra h2 {
    margin: 0 0 12px; font-size: 1.3rem; text-transform: uppercase; letter-spacing: 0.08em; color: var(--accent);
  }
  .hint { margin: -4px 0 14px; font-size: 1.1rem; color: var(--muted); }
</style>
