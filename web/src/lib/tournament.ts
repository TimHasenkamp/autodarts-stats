import type { Rule } from '$lib/api';

export const defaultRule = (): Rule => ({ variant: 'X01', base_score: 501, first_to: 2 });

// Wie server/internal/tournament: Zahl der Hauptrunden und ihre Namen.
export function roundCount(n: number): number {
  let r = 0;
  let s = 1;
  while (s < n) {
    s *= 2;
    r++;
  }
  return r;
}

export function roundName(rounds: number, r: number): string {
  switch (rounds - r) {
    case 0: return 'Finale';
    case 1: return 'Halbfinale';
    case 2: return 'Viertelfinale';
    case 3: return 'Achtelfinale';
  }
  return `Runde ${r}`;
}

/** Mögliche Einstiegsrunden des Lucky Losers als Abstand zum Finale (frühestens Runde 2). */
export function entryOptions(players: number): { distance: number; name: string }[] {
  const rounds = roundCount(players);
  const out = [];
  for (let d = 0; rounds - d >= 2; d++) out.push({ distance: d, name: roundName(rounds, rounds - d) });
  return out;
}

export function ruleText(r: Rule | undefined): string {
  if (!r) return '';
  const game = r.variant === 'X01' ? String(r.base_score || 501) : r.variant;
  return `${game} · First to ${r.first_to} (Best of ${2 * r.first_to - 1})`;
}

export const statusText: Record<string, string> = {
  draft: 'in Vorbereitung',
  running: 'läuft',
  finished: 'beendet',
};
