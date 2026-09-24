-- Turniere (K.-o.-System, optional mit Lucky-Loser-Runde und Spiel um Platz 3).
-- Der Turnierbaum wird nicht gespeichert, sondern aus Auslosung + Ergebnissen
-- berechnet (internal/tournament). Gespeichert werden nur Eingaben.
CREATE TABLE tournaments (
    id           INTEGER PRIMARY KEY,
    name         TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'draft', -- draft | running | finished
    third_place  INTEGER NOT NULL DEFAULT 0,
    lucky_loser  INTEGER NOT NULL DEFAULT 0,
    -- Einstiegsrunde des Lucky Losers als Abstand zum Finale (0 = Finale, 1 = Halbfinale, ...).
    ll_entry     INTEGER NOT NULL DEFAULT 1,
    -- Ausgeloster Gegner des Lucky Losers: Spiel und Seite in der Einstiegsrunde.
    playin_match INTEGER NOT NULL DEFAULT 0,
    playin_side  INTEGER NOT NULL DEFAULT 0,
    rules_json   TEXT NOT NULL DEFAULT '{}',
    created_at   TEXT NOT NULL,
    started_at   TEXT,
    finished_at  TEXT
);

-- position = Platz in der Auslosung (vor dem Start: Reihenfolge der Anmeldung).
CREATE TABLE tournament_players (
    tournament_id INTEGER NOT NULL REFERENCES tournaments(id) ON DELETE CASCADE,
    player_id     INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    position      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (tournament_id, player_id)
);
CREATE INDEX idx_tournament_players_player ON tournament_players(player_id);

-- Ergebnis einer Paarung. match_key benennt die Stelle im Baum (M2-0, L1-3, P, T).
-- player1/2 halten fest, fuer welche Paarung das Ergebnis gilt; passt die
-- berechnete Paarung nicht mehr (Korrektur weiter vorne), verfaellt es.
CREATE TABLE tournament_results (
    tournament_id INTEGER NOT NULL REFERENCES tournaments(id) ON DELETE CASCADE,
    match_key     TEXT NOT NULL,
    player1_id    INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    player2_id    INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    winner_id     INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    legs1         INTEGER NOT NULL DEFAULT 0,
    legs2         INTEGER NOT NULL DEFAULT 0,
    match_id      INTEGER REFERENCES matches(id) ON DELETE SET NULL,
    source        TEXT NOT NULL, -- auto | link | manual
    warning       TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    PRIMARY KEY (tournament_id, match_key)
);
CREATE INDEX idx_tournament_results_match ON tournament_results(match_id);

-- Matches, deren automatische Zuordnung der Admin geloest hat. Sie werden
-- fuer dieses Turnier nicht erneut automatisch zugeordnet.
CREATE TABLE tournament_ignored (
    tournament_id INTEGER NOT NULL REFERENCES tournaments(id) ON DELETE CASCADE,
    match_id      INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    PRIMARY KEY (tournament_id, match_id)
);
