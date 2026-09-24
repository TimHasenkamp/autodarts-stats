<script lang="ts">
  import { api, type PlayerRow, type Rule, type TournamentInput, type TournamentView } from '$lib/api';
  import { defaultRule, entryOptions, roundCount, roundName } from '$lib/tournament';

  let {
    initial = null,
    submitLabel = 'Speichern',
    onsave,
  }: { initial?: TournamentView | null; submitLabel?: string; onsave: (input: TournamentInput) => Promise<void> } = $props();

  // Nach dem Start stehen Teilnehmer und Modus fest (Auslosung).
  const locked = $derived(!!initial && initial.status !== 'draft');

  let players: PlayerRow[] = $state([]);
  let q = $state('');
  let busy = $state(false);
  let form = $state(fromInitial());

  function fromInitial() {
    const i = initial;
    return {
      name: i?.name ?? '',
      selected: new Set<number>(i?.standings.map((s) => s.player_id) ?? []),
      newNames: '',
      third_place: i?.third_place ?? true,
      lucky_loser: i?.lucky_loser ?? false,
      ll_entry: i?.ll_entry ?? 1,
      rounds: (i?.rules.rounds?.length ? i.rules.rounds : [{ ...defaultRule(), first_to: 3 }]).map((r) => ({ ...r })) as Rule[],
      lucky: { ...(i?.rules.lucky_loser?.first_to ? i.rules.lucky_loser : defaultRule()) } as Rule,
    };
  }

  api.get<PlayerRow[]>('/api/players').then((p) => (players = p)).catch(() => {});

  const newList = $derived(form.newNames.split('\n').map((s) => s.trim()).filter(Boolean));
  const count = $derived(locked ? (initial?.players ?? 0) : form.selected.size + newList.length);
  const rounds = $derived(Math.max(1, roundCount(Math.max(count, 2))));
  const entries = $derived(entryOptions(count));
  const filtered = $derived(players.filter((p) => p.display_name.toLowerCase().includes(q.toLowerCase())));

  // Regeln pro Runde; rounds[0] = Finale. Neue frühe Runden übernehmen die bisher früheste.
  $effect(() => {
    while (form.rounds.length < rounds) {
      const last = form.rounds[form.rounds.length - 1];
      form.rounds.push({ ...last, first_to: form.rounds.length === 0 ? 3 : Math.min(last.first_to, 2) });
    }
  });

  function toggle(id: number) {
    const s = new Set(form.selected);
    if (s.has(id)) s.delete(id);
    else s.add(id);
    form.selected = s;
  }

  const thirdConflict = $derived(form.lucky_loser && form.third_place && form.ll_entry === 0);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    try {
      await onsave({
        name: form.name,
        player_ids: [...form.selected],
        new_names: newList,
        third_place: form.third_place,
        lucky_loser: form.lucky_loser,
        ll_entry: form.ll_entry,
        rules: { rounds: form.rounds.slice(0, Math.max(rounds, 1)), lucky_loser: form.lucky },
      });
    } finally {
      busy = false;
    }
  }
</script>

{#snippet ruleEditor(r: Rule)}
  <select bind:value={r.variant} aria-label="Variante">
    <option value="X01">X01</option>
    <option value="Cricket">Cricket</option>
    <option value="Bermuda">Bermuda</option>
    <option value="Shanghai">Shanghai</option>
    <option value="ATC">Around the Clock</option>
  </select>
  {#if r.variant === 'X01'}
    <select bind:value={r.base_score} aria-label="Startpunkte">
      {#each [121, 170, 301, 501, 701, 901] as s}<option value={s}>{s}</option>{/each}
    </select>
  {/if}
  <select bind:value={r.first_to} aria-label="Legs zum Sieg">
    {#each [1, 2, 3, 4, 5, 6, 7] as n}<option value={n}>First to {n} (Best of {2 * n - 1})</option>{/each}
  </select>
{/snippet}

<form class="card tform" onsubmit={submit}>
  <label class="full">Name <input bind:value={form.name} placeholder="z. B. Herbstcup 2026" required /></label>

  {#if !locked}
    <fieldset>
      <legend>Teilnehmer <span class="muted">({count})</span></legend>
      <input type="search" placeholder="Spieler suchen …" bind:value={q} />
      <div class="plist">
        {#each filtered as p (p.player_id)}
          <label class="chk"><input type="checkbox" checked={form.selected.has(p.player_id)} onchange={() => toggle(p.player_id)} /> {p.display_name}</label>
        {:else}
          <span class="muted">Keine Spieler gefunden.</span>
        {/each}
      </div>
      <label class="full">Neue Spieler <span class="muted">(ein Name pro Zeile; vorhandene Namen werden erkannt)</span>
        <textarea bind:value={form.newNames} rows="3" placeholder={'Max\nLisa'}></textarea>
      </label>
    </fieldset>

    <fieldset>
      <legend>Modus</legend>
      <label class="chk"><input type="checkbox" bind:checked={form.third_place} /> Spiel um Platz 3</label>
      <label class="chk"><input type="checkbox" bind:checked={form.lucky_loser} disabled={count > 0 && entries.length === 0} /> Lucky-Loser-Runde</label>
      {#if form.lucky_loser}
        <label>Einstieg des Lucky Losers
          <select bind:value={form.ll_entry}>
            {#each entries as o}<option value={o.distance}>vor dem {o.name}</option>{/each}
            {#if !entries.some((o) => o.distance === form.ll_entry)}<option value={form.ll_entry}>(passt nicht zur Teilnehmerzahl)</option>{/if}
          </select>
        </label>
        <p class="muted">
          Wer vor dieser Runde verliert, spielt in der Lucky-Loser-Runde weiter. Der Sieger spielt ein Zusatzspiel gegen
          einen zugelosten Qualifikanten; wer gewinnt, zieht in die Runde ein.
        </p>
        {#if thirdConflict}<p class="error">Mit Einstieg vor dem Finale gibt es kein Spiel um Platz 3 (die Halbfinal-Verlierer spielen dann die Lucky-Loser-Runde).</p>{/if}
      {/if}
    </fieldset>
  {/if}

  <fieldset>
    <legend>Spielregeln</legend>
    {#each Array.from({ length: rounds }, (_, i) => i + 1) as r (r)}
      {@const rule = form.rounds[rounds - r]}
      {#if rule}<div class="rule"><span class="rname">{roundName(rounds, r)}</span>{@render ruleEditor(rule)}</div>{/if}
    {/each}
    {#if form.lucky_loser || initial?.lucky_loser}
      <div class="rule"><span class="rname">Lucky-Loser-Runde</span>{@render ruleEditor(form.lucky)}</div>
    {/if}
    <p class="muted">Spiel um Platz 3 wie Halbfinale, Zusatzspiel wie die Runde vor dem Einstieg. Abweichend gespielte Matches werden trotzdem gewertet, aber markiert.</p>
  </fieldset>

  <div class="row">
    <button type="submit" disabled={busy || !form.name.trim() || thirdConflict}>{submitLabel}</button>
  </div>
</form>

<style>
  .tform { display: flex; flex-direction: column; gap: 12px; }
  fieldset { border: 1px solid var(--border); border-radius: 8px; padding: 8px 10px; display: flex; flex-direction: column; gap: 8px; margin: 0; }
  legend { font-weight: 600; padding: 0 4px; }
  label { display: flex; flex-direction: column; gap: 2px; font-size: 0.85rem; color: var(--muted); }
  label.chk { flex-direction: row; align-items: center; gap: 6px; color: var(--text); font-size: 0.95rem; }
  label.chk input { min-height: 0; width: auto; }
  .full input, .full textarea { width: 100%; }
  textarea { font: inherit; color: inherit; background: var(--bg); border: 1px solid var(--border); border-radius: 6px; padding: 8px 10px; }
  .plist { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: 4px 12px; max-height: 240px; overflow-y: auto; }
  .rule { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
  .rname { min-width: 140px; font-weight: 600; }
</style>
