<script lang="ts">
  import { page } from '$app/state';
  import { fade } from 'svelte/transition';
  import { api, type TParticipant, type TRound, type TStatRow, type TournamentView } from '$lib/api';
  import Bracket from '$lib/components/Bracket.svelte';
  import FitToScreen from '$lib/components/FitToScreen.svelte';
  import { num } from '$lib/format';
  import { ruleText } from '$lib/tournament';

  // Diashow für einen Bildschirm im Raum. Jede Folie wird auf den Bildschirm
  // skaliert. URL-Parameter: ?dauer=15 (Sekunden pro Folie), ?folie=baum (feste Folie).
  // Tasten: ← → blättern, Leertaste Pause, F Vollbild.
  type SlideID = 'podium' | 'next' | 'baum' | 'll' | 'tabelle';
  const titles: Record<SlideID, string> = {
    podium: 'Siegerehrung',
    next: 'Als Nächstes',
    baum: 'Turnierbaum',
    ll: 'Lucky-Loser-Runde',
    tabelle: 'Spieler & Statistik',
  };

  let view: TournamentView | null = $state(null);
  let error = $state('');
  let idx = $state(0);
  let paused = $state(false);
  let clock = $state('');
  let idle = $state(false);

  const duration = $derived(Math.max(5, Number(page.url.searchParams.get('dauer')) || 15));
  const pinned = $derived(page.url.searchParams.get('folie') as SlideID | null);

  async function load() {
    try {
      view = await api.get<TournamentView>(`/api/tournaments/${page.params.id}`);
      error = '';
    } catch (e) {
      error = (e as Error).message;
    }
  }
  load();

  const llRounds = $derived.by((): TRound[] => {
    if (!view) return [];
    const rs = [...view.lucky_rounds];
    if (view.playin) rs.push({ name: 'Zusatzspiel', rule: view.playin.rule, matches: [view.playin] });
    return rs;
  });

  const slides = $derived.by((): SlideID[] => {
    const v = view;
    if (!v || v.status === 'draft') return [];
    const out: SlideID[] = [];
    if (v.champion) out.push('podium');
    if (v.next.length) out.push('next');
    out.push('baum');
    if (llRounds.length) out.push('ll');
    out.push('tabelle');
    if (pinned && out.includes(pinned)) return [pinned];
    return out;
  });
  const current = $derived(slides.length ? slides[idx % slides.length] : null);

  const podium = $derived.by(() => {
    const s = view?.standings ?? [];
    const at = (p: number) => s.filter((x) => x.place === p);
    return { first: at(1), second: at(2), third: at(3) };
  });
  const statsBy = $derived.by(() => {
    const rows: TStatRow[] = (view as TournamentView | null)?.stats?.rows ?? [];
    return new Map(rows.map((r) => [r.player_id, r]));
  });

  function go(step: number) {
    if (slides.length) idx = (idx + step + slides.length) % slides.length;
  }

  // Folienwechsel; ein neuer Timer pro Folie, damit der Fortschrittsbalken passt.
  // Nur an der Folienfolge hängen, nicht am Array: das wird bei jedem
  // Neuladen neu erzeugt und würde den Timer sonst zurücksetzen.
  const slideCount = $derived(slides.length);
  $effect(() => {
    void idx;
    if (paused || slideCount < 2) return;
    const t = setTimeout(() => go(1), duration * 1000);
    return () => clearTimeout(t);
  });
  $effect(() => {
    const t = setInterval(load, 10000);
    return () => clearInterval(t);
  });
  $effect(() => {
    const tick = () => (clock = new Date().toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit' }));
    tick();
    const t = setInterval(tick, 5000);
    return () => clearInterval(t);
  });
  // Mauszeiger nach kurzer Zeit ausblenden.
  let idleTimer: ReturnType<typeof setTimeout>;
  function wake() {
    idle = false;
    clearTimeout(idleTimer);
    idleTimer = setTimeout(() => (idle = true), 3000);
  }

  function key(e: KeyboardEvent) {
    if (e.key === 'ArrowRight') go(1);
    else if (e.key === 'ArrowLeft') go(-1);
    else if (e.key === ' ') { paused = !paused; e.preventDefault(); }
    else if (e.key === 'f' || e.key === 'F') fullscreen();
    else return;
    wake();
  }
  function fullscreen() {
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
    else document.documentElement.requestFullscreen?.().catch(() => {});
  }
</script>

<svelte:head><title>{view?.name ?? 'Turnier'} · Your Darts</title></svelte:head>
<svelte:window onkeydown={key} onmousemove={wake} />

{#snippet person(p: TParticipant, big = false)}
  <div class="pname" class:big>{p.display_name}</div>
{/snippet}

<div class="beamer" class:idle>
  <header>
    <div class="brand"><img src="/favicon.svg" alt="" width="34" height="34" /> <span>{view?.name ?? 'Turnier'}</span></div>
    <div class="slide-title">{current ? titles[current] : ''}</div>
    <div class="right">
      {#if slides.length > 1}
        <div class="dots">
          {#each slides as s, i}
            <button class="dot" class:on={s === current} onclick={() => { idx = i; wake(); }} aria-label={titles[s]}></button>
          {/each}
        </div>
      {/if}
      <span class="clock">{clock}</span>
      <button class="icon" onclick={fullscreen} title="Vollbild (F)">⛶</button>
    </div>
  </header>

  <main>
    {#if !view}
      <p class="msg">{error || 'Lade …'}</p>
    {:else if view.status === 'draft'}
      <p class="msg">Das Turnier ist noch nicht gestartet.</p>
    {:else}
      {@const v = view}
      {#key current}
        <div class="slide" in:fade={{ duration: 450, delay: 150 }} out:fade={{ duration: 250 }}>
          {#if current === 'podium'}
            <FitToScreen maxScale={1.4}>
              <div class="podium">
                <div class="step second">
                  {#each podium.second as p}{@render person(p)}{/each}
                  <div class="block"><span>2</span></div>
                </div>
                <div class="step first">
                  <div class="crown">🏆</div>
                  {#each podium.first as p}{@render person(p, true)}{/each}
                  <div class="block"><span>1</span></div>
                </div>
                <div class="step third">
                  {#each podium.third as p}<div class="pname">{p.display_name}<small>{p.label}</small></div>{/each}
                  <div class="block"><span>3</span></div>
                </div>
              </div>
            </FitToScreen>
          {:else if current === 'next'}
            <FitToScreen maxScale={1.5}>
              <div class="nextlist" style:--cols={Math.min(v.next.length, v.next.length > 4 ? 3 : 2)}>
                {#each v.next as m (m.key)}
                  <div class="nm">
                    <div class="nm-head"><span>Spiel {m.no}</span><span>{ruleText(m.rule)}</span></div>
                    <div class="pair">
                      <span class="pl">{m.a.display_name}</span>
                      <span class="vs">vs</span>
                      <span class="pl">{m.b.display_name}</span>
                    </div>
                  </div>
                {/each}
              </div>
            </FitToScreen>
          {:else if current === 'baum'}
            <FitToScreen>
              <div class="bracket-slide">
                <Bracket rounds={v.main} big />
                {#if v.third}
                  <div class="third"><Bracket rounds={[{ name: 'Spiel um Platz 3', rule: v.third.rule, matches: [v.third] }]} big /></div>
                {/if}
              </div>
            </FitToScreen>
          {:else if current === 'll'}
            <FitToScreen>
              <div class="bracket-slide">
                {#if v.ll_entry_name}<p class="hint">Der Sieger zieht über das Zusatzspiel ins {v.ll_entry_name} ein.</p>{/if}
                <Bracket rounds={llRounds} big />
              </div>
            </FitToScreen>
          {:else if current === 'tabelle'}
            {@const s = v.stats}
            <FitToScreen maxScale={1.4}>
              <div class="table-slide">
                {#if s && (s.best_average || s.most_180 || s.highest_checkout)}
                  <div class="leaders">
                    <div><span class="lbl">Bester Average</span><span class="val">{num(s.best_average?.value)}</span><span class="who">{s.best_average?.display_name ?? '–'}</span></div>
                    <div><span class="lbl">Bestes Match</span><span class="val">{num(s.best_match_average?.value)}</span><span class="who">{s.best_match_average?.display_name ?? '–'}</span></div>
                    <div><span class="lbl">Meiste 180er</span><span class="val">{s.most_180?.value ?? '–'}</span><span class="who">{s.most_180?.display_name ?? '–'}</span></div>
                    <div><span class="lbl">Höchstes Finish</span><span class="val">{s.highest_checkout?.value ?? '–'}</span><span class="who">{s.highest_checkout?.display_name ?? '–'}</span></div>
                  </div>
                {/if}
                <table>
                  <thead><tr><th>Platz</th><th>Spieler</th><th>Status</th><th>Sp</th><th>S</th><th>Legs</th><th>Avg</th><th>180</th><th>High CO</th></tr></thead>
                  <tbody>
                    {#each v.standings as p (p.player_id)}
                      {@const r = statsBy.get(p.player_id)}
                      <tr class:out={p.out && p.place !== 1}>
                        <td class="place">{p.place || '–'}</td>
                        <td class="name">{p.display_name}{#if p.lucky_loser} <span class="ll">LL</span>{/if}</td>
                        <td class="status">{p.label}</td>
                        <td>{r?.matches ?? 0}</td><td>{r?.wins ?? 0}</td>
                        <td>{r ? `${r.legs_won}:${r.legs_lost}` : '–'}</td>
                        <td>{num(r?.average)}</td><td>{r?.count_180 ?? 0}</td><td>{r?.highest_checkout || '–'}</td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            </FitToScreen>
          {/if}
        </div>
      {/key}
    {/if}
  </main>

  {#if slides.length > 1}
    {#key `${idx}-${paused}`}
      <div class="progress" class:paused style:animation-duration="{duration}s"></div>
    {/key}
  {/if}
  {#if paused}<div class="paused-note">Pausiert · Leertaste</div>{/if}
</div>

<style>
  .beamer { position: fixed; inset: 0; display: flex; flex-direction: column; background: var(--bg); overflow: hidden; }
  .beamer.idle, .beamer.idle * { cursor: none; }

  header {
    position: relative; display: grid; grid-template-columns: 1fr auto 1fr; align-items: center; gap: 24px;
    padding: 16px 32px; background: var(--navy); color: #fff;
    background-image: radial-gradient(80% 260% at 100% 0%, rgba(0, 153, 154, 0.35) 0%, rgba(0, 153, 154, 0) 60%);
  }
  header::after { content: ''; position: absolute; left: 0; right: 0; bottom: 0; height: 3px; background: var(--brand-line); }
  .brand { display: flex; align-items: center; gap: 14px; font-family: var(--font-heading); font-weight: 700; font-size: 1.7rem; min-width: 0; }
  .brand span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .brand img { filter: brightness(0) invert(1); flex: none; }
  .slide-title { font-family: var(--font-heading); font-size: 1.35rem; letter-spacing: 0.04em; color: #9fe6dc; text-transform: uppercase; }
  .right { display: flex; align-items: center; justify-content: flex-end; gap: 22px; }
  .dots { display: flex; gap: 8px; }
  .dot { width: 10px; height: 10px; min-height: 0; padding: 0; border-radius: 50%; background: rgba(255, 255, 255, 0.3); border: none; }
  .dot.on { background: #fff; width: 26px; border-radius: 5px; }
  .dot, .dot.on { transition: width 0.3s, background-color 0.3s; }
  .clock { font-family: var(--font-heading); font-size: 1.5rem; font-variant-numeric: tabular-nums; }
  .icon { background: rgba(255, 255, 255, 0.1); color: #fff; border: 1px solid rgba(255, 255, 255, 0.25); padding: 4px 12px; font-size: 1.2rem; }

  main { position: relative; flex: 1; min-height: 0; }
  .slide { position: absolute; inset: 0; padding: 36px 44px; }
  .msg { display: grid; place-items: center; height: 100%; margin: 0; font-size: 1.6rem; color: var(--muted); }

  .progress { position: absolute; left: 0; bottom: 0; height: 4px; width: 100%; background: var(--brand-line); transform-origin: left; animation: grow linear forwards; }
  .progress.paused { animation-play-state: paused; }
  @keyframes grow { from { transform: scaleX(0); } to { transform: scaleX(1); } }
  .paused-note { position: absolute; right: 24px; bottom: 16px; font-size: 0.9rem; color: var(--muted); }

  /* Als Nächstes */
  .nextlist { display: grid; grid-template-columns: repeat(var(--cols), 520px); gap: 22px; }
  .nm { background: var(--card); border: 1px solid var(--border); border-left: 4px solid var(--accent); border-radius: var(--radius); padding: 22px 28px; box-shadow: var(--shadow); }
  .nm-head { display: flex; justify-content: space-between; font-size: 0.95rem; color: var(--muted); letter-spacing: 0.04em; }
  .pair { display: flex; align-items: baseline; gap: 16px; margin-top: 10px; font-size: 2.2rem; font-weight: 600; color: var(--heading); }
  .pair .vs { font-family: var(--font-heading); font-size: 1.1rem; color: var(--muted); font-weight: 400; }

  /* Turnierbaum */
  .bracket-slide { display: flex; flex-direction: column; gap: 28px; min-width: 1100px; }
  .third { max-width: 480px; }
  .hint { margin: 0; font-size: 1.1rem; color: var(--muted); }

  /* Siegerehrung */
  .podium { display: flex; align-items: flex-end; gap: 28px; padding-top: 20px; }
  .step { display: flex; flex-direction: column; align-items: center; gap: 12px; width: 320px; }
  .block {
    width: 100%; display: grid; place-items: center; border-radius: var(--radius) var(--radius) 0 0; color: #fff;
    font-family: var(--font-heading); font-weight: 700; font-size: 3.5rem; background: var(--navy); position: relative; overflow: hidden;
    background-image: radial-gradient(120% 120% at 50% 0%, rgba(0, 153, 154, 0.45) 0%, rgba(0, 153, 154, 0) 70%);
  }
  .block::before { content: ''; position: absolute; inset: 0 0 auto 0; height: 4px; background: var(--brand-line); }
  .first .block { height: 260px; }
  .second .block { height: 190px; }
  .third .block { height: 140px; }
  .crown { font-size: 3.5rem; }
  .pname { font-size: 1.9rem; font-weight: 600; color: var(--heading); text-align: center; display: flex; flex-direction: column; }
  .pname.big { font-family: var(--font-heading); font-size: 3rem; }
  .pname small { font-size: 0.9rem; font-weight: 400; color: var(--muted); }

  /* Spieler & Statistik */
  .table-slide { display: flex; flex-direction: column; gap: 28px; width: 1200px; }
  .leaders { display: grid; grid-template-columns: repeat(4, 1fr); gap: 1px; background: var(--border); border-radius: var(--radius); overflow: hidden; }
  .leaders > div { background: var(--card); padding: 18px 22px; display: flex; flex-direction: column; }
  .lbl { font-size: 0.8rem; text-transform: uppercase; letter-spacing: 0.08em; color: var(--muted); }
  .val { font-family: var(--font-heading); font-size: 2.4rem; font-weight: 700; color: var(--heading); }
  .who { font-size: 1.05rem; color: var(--accent); font-weight: 500; }
  table { font-size: 1.3rem; background: var(--card); border-radius: var(--radius); overflow: hidden; box-shadow: var(--shadow); }
  th { font-size: 0.8rem; padding: 12px 16px; }
  td { padding: 12px 16px; }
  td.place { font-family: var(--font-heading); font-weight: 700; color: var(--heading); }
  td.name { font-weight: 600; color: var(--heading); text-align: left; }
  td.status { text-align: left; color: var(--muted); font-size: 1.05rem; }
  th:nth-child(2), th:nth-child(3) { text-align: left; }
  tr.out td { opacity: 0.6; }
  .ll { font-size: 0.75rem; border: 1px solid var(--border-strong); border-radius: 999px; padding: 1px 8px; color: var(--muted); vertical-align: middle; }
</style>
