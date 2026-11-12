-- Turniere: Teilnehmer im laufenden Turnier aendern (Ersatz, Tausch, Rueckzug,
-- Nachmelder), stabiler Matchende-Zeitpunkt und Schutz vor dem Loeschen von
-- Spielern, die in einem Turnier stehen.

-- Matchende: wird nur beim Uebergang auf finished gesetzt (updated_at aendert
-- sich bei jedem erneut gesendeten Stand). Backfill mit dem letzten Stand.
ALTER TABLE matches ADD COLUMN finished_at TEXT;
UPDATE matches SET finished_at = updated_at WHERE finished = 1;

-- tournament_players neu aufbauen: Spieler-Referenz RESTRICT statt CASCADE
-- (Entwuerfe raeumt die API vor dem Loeschen selbst auf), dazu
--   bye_slot:        Nachmelder, nimmt das Freilos in Spiel bye_slot (Runde 1, 0-basiert) ein
--   withdrawn_at:    Zeitpunkt des Rueckzugs (NULL = spielt mit)
--   withdraw_reason: retired | dq
CREATE TABLE tournament_players_new (
    tournament_id   INTEGER NOT NULL REFERENCES tournaments(id) ON DELETE CASCADE,
    player_id       INTEGER NOT NULL REFERENCES players(id) ON DELETE RESTRICT,
    position        INTEGER NOT NULL DEFAULT 0,
    bye_slot        INTEGER,
    withdrawn_at    TEXT,
    withdraw_reason TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (tournament_id, player_id)
);
INSERT INTO tournament_players_new (tournament_id, player_id, position)
    SELECT tournament_id, player_id, position FROM tournament_players;
DROP TABLE tournament_players;
ALTER TABLE tournament_players_new RENAME TO tournament_players;
CREATE INDEX idx_tournament_players_player ON tournament_players(player_id);

-- tournament_results neu aufbauen: RESTRICT fuer Spieler, dazu outcome
-- ('' regulaer | retired = Aufgabe | dq = Disqualifikation des Verlierers).
CREATE TABLE tournament_results_new (
    tournament_id INTEGER NOT NULL REFERENCES tournaments(id) ON DELETE CASCADE,
    match_key     TEXT NOT NULL,
    player1_id    INTEGER NOT NULL REFERENCES players(id) ON DELETE RESTRICT,
    player2_id    INTEGER NOT NULL REFERENCES players(id) ON DELETE RESTRICT,
    winner_id     INTEGER NOT NULL REFERENCES players(id) ON DELETE RESTRICT,
    legs1         INTEGER NOT NULL DEFAULT 0,
    legs2         INTEGER NOT NULL DEFAULT 0,
    match_id      INTEGER REFERENCES matches(id) ON DELETE SET NULL,
    source        TEXT NOT NULL, -- auto | link | manual
    warning       TEXT NOT NULL DEFAULT '',
    outcome       TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    PRIMARY KEY (tournament_id, match_key)
);
INSERT INTO tournament_results_new (tournament_id, match_key, player1_id, player2_id, winner_id, legs1, legs2, match_id, source, warning, created_at)
    SELECT tournament_id, match_key, player1_id, player2_id, winner_id, legs1, legs2, match_id, source, warning, created_at FROM tournament_results;
DROP TABLE tournament_results;
ALTER TABLE tournament_results_new RENAME TO tournament_results;
CREATE INDEX idx_tournament_results_match ON tournament_results(match_id);

-- Fruehere Spieler eines Platzes (seat = Platz im Baum: Spiel i der ersten
-- Runde, 2i oben, 2i+1 unten). Ihre gespielten Ergebnisse zaehlen fuer den
-- Platz weiter; offene und kuenftige Paarungen spielt der aktuelle Spieler
-- aus tournament_players.
CREATE TABLE tournament_former (
    tournament_id INTEGER NOT NULL REFERENCES tournaments(id) ON DELETE CASCADE,
    seat          INTEGER NOT NULL,
    player_id     INTEGER NOT NULL REFERENCES players(id) ON DELETE RESTRICT,
    replaced_by   INTEGER NOT NULL REFERENCES players(id) ON DELETE RESTRICT,
    created_at    TEXT NOT NULL,
    PRIMARY KEY (tournament_id, seat, player_id)
);
CREATE INDEX idx_tournament_former_player ON tournament_former(player_id);
CREATE INDEX idx_tournament_former_replaced ON tournament_former(replaced_by);
