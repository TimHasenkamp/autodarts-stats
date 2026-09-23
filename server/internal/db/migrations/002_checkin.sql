-- Geschuetzte Spieler: Matches unter ihrem Namen zaehlen nur mit gueltigem
-- Check-in-Code oder passender Autodarts-User-ID, sonst Freigabe-Queue.
ALTER TABLE players ADD COLUMN protected INTEGER NOT NULL DEFAULT 0;

CREATE TABLE player_chips (
    id           INTEGER PRIMARY KEY,
    player_id    INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    uid_hash     TEXT NOT NULL UNIQUE,
    label        TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,
    last_used_at TEXT
);
CREATE INDEX idx_player_chips_player ON player_chips(player_id);

-- Chips, die eingestempelt wurden, aber noch keinem Spieler gehoeren.
CREATE TABLE unknown_chips (
    uid_hash      TEXT PRIMARY KEY,
    uid_hint      TEXT NOT NULL DEFAULT '',
    board_id      INTEGER REFERENCES boards(id) ON DELETE SET NULL,
    first_seen_at TEXT NOT NULL,
    last_seen_at  TEXT NOT NULL,
    seen_count    INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE checkins (
    id         INTEGER PRIMARY KEY,
    player_id  INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    board_id   INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    token      TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
CREATE INDEX idx_checkins_board_token ON checkins(board_id, token);
CREATE INDEX idx_checkins_expires ON checkins(expires_at);

-- Manuelle bzw. per Check-in erzeugte Zuordnung eines Match-Slots zu einem
-- Spieler. player_id NULL = Slot ignorieren. Ueberlebt reprocess.
CREATE TABLE match_overrides (
    match_id     INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    player_index INTEGER NOT NULL,
    player_id    INTEGER REFERENCES players(id) ON DELETE CASCADE,
    source       TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    PRIMARY KEY (match_id, player_index)
);

-- Slots, die auf Freigabe warten (geschuetzter Name ohne gueltigen Code).
CREATE TABLE match_pending (
    match_id     INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    player_index INTEGER NOT NULL,
    name         TEXT NOT NULL,
    user_id      TEXT NOT NULL DEFAULT '',
    suggested_id INTEGER REFERENCES players(id) ON DELETE SET NULL,
    reason       TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    PRIMARY KEY (match_id, player_index)
);
