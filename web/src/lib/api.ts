export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: 'same-origin',
  });
  const text = await res.text();
  let data: unknown = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = text;
  }
  if (!res.ok) {
    const msg = data && typeof data === 'object' && 'error' in data ? String((data as { error: string }).error) : res.statusText;
    throw new ApiError(res.status, msg);
  }
  return data as T;
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body ?? {}),
  put: <T>(path: string, body: unknown) => request<T>('PUT', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
};

export interface PlayerRow {
  player_id: number;
  display_name: string;
  matches: number;
  wins: number;
  win_rate: number | null;
  average: number | null;
  best_match_average: number | null;
  first9_avg: number | null;
  checkout_rate: number | null;
  highest_checkout: number;
  count_180: number;
  count_140plus: number;
  count_100plus: number;
  legs_won: number;
  legs_played: number;
  darts: number;
  open_matches: number;
  last_played_at?: string;
}

export interface MatchPlayerInfo {
  player_id: number;
  display_name: string;
  won: boolean;
  average: number | null;
  first9_avg: number | null;
  checkout_rate: number | null;
  highest_checkout: number;
  count_180: number;
  count_140plus: number;
  count_100plus: number;
  legs_won: number;
  legs_played: number;
}

export interface MatchSummary {
  match_id: number;
  played_at: string;
  variant: string;
  finished: boolean;
  board_name?: string;
  players: MatchPlayerInfo[];
}

export interface LegRow {
  set: number;
  leg: number;
  player_id: number;
  display_name: string;
  won: boolean;
  darts: number;
  average: number | null;
  first9_avg: number | null;
  checkout: number;
  count_180: number;
  count_140plus: number;
  count_100plus: number;
}

export interface MatchDetail extends MatchSummary {
  autodarts_match_id: string;
  settings: string;
  legs: LegRow[];
  pending: { player_index: number; name: string; reason: string }[];
}

export interface HistoryPoint {
  match_id: number;
  played_at: string;
  average: number | null;
  won: boolean;
}

export interface Profile {
  player: PlayerRow;
  aliases: string[];
  has_account: boolean;
  best_leg_average: number | null;
  best_leg_darts: number;
  most_180_match: number;
  variants: { variant: string; matches: number; wins: number }[];
  history: HistoryPoint[];
  recent_matches: MatchSummary[];
}

export interface H2H {
  a: PlayerRow;
  b: PlayerRow;
  matches: number;
  wins_a: number;
  wins_b: number;
  legs_a: number;
  legs_b: number;
  average_a: number | null;
  average_b: number | null;
  recent_matches: MatchSummary[];
}

export interface Meta {
  players: number;
  matches: number;
  legs: number;
  variants: string[];
}

export interface Filter {
  from?: string;
  to?: string;
  variant?: string;
  min_matches?: number;
  sort?: string;
  order?: string;
}

export function qs(f: Record<string, string | number | undefined | null>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(f)) {
    if (v !== undefined && v !== null && v !== '') p.set(k, String(v));
  }
  const s = p.toString();
  return s ? '?' + s : '';
}

// ---- Turniere ----

export interface Rule {
  variant: string;
  base_score: number;
  first_to: number;
}

export interface Rules {
  rounds: Rule[]; // [0] = Finale, [1] = Halbfinale, ...
  lucky_loser: Rule;
}

export interface TournamentSummary {
  id: number;
  name: string;
  status: 'draft' | 'running' | 'finished';
  created_at: string;
  started_at?: string;
  finished_at?: string;
  players: number;
  champion?: { player_id: number; display_name: string };
}

export interface TSide {
  player_id?: number;
  display_name?: string;
  placeholder?: string;
  legs?: number;
  won: boolean;
}

export interface TMatch {
  key: string;
  no: number;
  bracket: 'main' | 'll' | 'playin' | 'third';
  round: number;
  status: 'wait' | 'ready' | 'done' | 'walkover' | 'empty';
  rule: Rule;
  a: TSide;
  b: TSide;
  match_id?: number;
  source?: 'auto' | 'link' | 'manual';
  warning?: string;
  target?: string;
}

export interface TRound {
  name: string;
  rule: Rule;
  matches: TMatch[];
}

export interface Placement {
  place: number;
  label: string;
  lucky_loser: boolean;
  out: boolean;
}

export interface TParticipant extends Placement {
  player_id: number;
  display_name: string;
}

export interface TStatRow {
  player_id: number;
  display_name: string;
  matches: number;
  wins: number;
  legs_won: number;
  legs_lost: number;
  average: number | null;
  best_match_average: number | null;
  count_180: number;
  count_140plus: number;
  count_100plus: number;
  highest_checkout: number;
}

export interface TLeader {
  player_id: number;
  display_name: string;
  value: number;
}

export interface TournamentView extends TournamentSummary {
  third_place: boolean;
  lucky_loser: boolean;
  ll_entry: number;
  ll_entry_name?: string;
  rules: Rules;
  round_names: string[];
  main: TRound[];
  lucky_rounds: TRound[];
  playin?: TMatch;
  third?: TMatch;
  next: TMatch[];
  standings: TParticipant[];
  stats: {
    rows: TStatRow[];
    best_average: TLeader | null;
    best_match_average: TLeader | null;
    most_180: TLeader | null;
    highest_checkout: TLeader | null;
  } | null;
}

export interface TournamentInput {
  name: string;
  player_ids: number[];
  new_names: string[];
  third_place: boolean;
  lucky_loser: boolean;
  ll_entry: number;
  rules: Rules;
}

export interface PlayerTournaments {
  tournaments: (Placement & {
    tournament_id: number;
    name: string;
    status: string;
    started_at?: string;
    players: number;
    matches: number;
    wins: number;
  })[];
  wins: number;
  podium: number;
}

export interface Candidate {
  match_id: number;
  played_at: string;
  variant: string;
  score: string;
  winner: string;
  linked_to?: string;
}
