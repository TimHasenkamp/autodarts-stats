CREATE TABLE boards (
    id           INTEGER PRIMARY KEY,
    name         TEXT NOT NULL UNIQUE,
    api_key_hash TEXT NOT NULL UNIQUE,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_seen_at TEXT
);

CREATE TABLE players (
    id                INTEGER PRIMARY KEY,
    display_name      TEXT NOT NULL,
    normalized_name   TEXT NOT NULL UNIQUE,
    autodarts_user_id TEXT UNIQUE,
    created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE player_aliases (
    normalized_name TEXT PRIMARY KEY,
    player_id       INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE
);
CREATE INDEX idx_player_aliases_player ON player_aliases(player_id);

CREATE TABLE matches (
    id                 INTEGER PRIMARY KEY,
    autodarts_match_id TEXT NOT NULL UNIQUE,
    board_id           INTEGER REFERENCES boards(id) ON DELETE SET NULL,
    played_at          TEXT NOT NULL,
    variant            TEXT NOT NULL DEFAULT '',
    settings_json      TEXT NOT NULL DEFAULT '{}',
    finished           INTEGER NOT NULL DEFAULT 0,
    last_set           INTEGER NOT NULL DEFAULT 1,
    last_leg           INTEGER NOT NULL DEFAULT 1,
    raw_json           TEXT NOT NULL,
    received_at        TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);
CREATE INDEX idx_matches_played_at ON matches(played_at);
CREATE INDEX idx_matches_variant ON matches(variant);

-- Endstand eines Legs (letzter Snapshot bevor das naechste Leg begann bzw. Matchende).
CREATE TABLE match_snapshots (
    id          INTEGER PRIMARY KEY,
    match_id    INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    set_no      INTEGER NOT NULL,
    leg_no      INTEGER NOT NULL,
    raw_json    TEXT NOT NULL,
    received_at TEXT NOT NULL,
    UNIQUE(match_id, set_no, leg_no)
);

CREATE TABLE match_players (
    match_id          INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    player_id         INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    player_index      INTEGER NOT NULL,
    won               INTEGER NOT NULL DEFAULT 0,
    average           REAL,
    first9_avg        REAL,
    checkout_rate     REAL,
    checkouts_hit     INTEGER NOT NULL DEFAULT 0,
    checkout_attempts INTEGER NOT NULL DEFAULT 0,
    highest_checkout  INTEGER NOT NULL DEFAULT 0,
    count_180         INTEGER NOT NULL DEFAULT 0,
    count_140plus     INTEGER NOT NULL DEFAULT 0,
    count_100plus     INTEGER NOT NULL DEFAULT 0,
    legs_won          INTEGER NOT NULL DEFAULT 0,
    legs_played       INTEGER NOT NULL DEFAULT 0,
    darts_thrown      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (match_id, player_id)
);
CREATE INDEX idx_match_players_player ON match_players(player_id);

CREATE TABLE match_legs (
    match_id      INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    set_no        INTEGER NOT NULL,
    leg_no        INTEGER NOT NULL,
    player_id     INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    won           INTEGER NOT NULL DEFAULT 0,
    darts         INTEGER NOT NULL DEFAULT 0,
    points        INTEGER,
    average       REAL,
    first9_avg    REAL,
    checkout      INTEGER NOT NULL DEFAULT 0,
    checkouts_hit     INTEGER NOT NULL DEFAULT 0,
    checkout_attempts INTEGER NOT NULL DEFAULT 0,
    count_180     INTEGER NOT NULL DEFAULT 0,
    count_140plus INTEGER NOT NULL DEFAULT 0,
    count_100plus INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (match_id, set_no, leg_no, player_id)
);
CREATE INDEX idx_match_legs_player ON match_legs(player_id);

-- Payloads, die kein Parser erkannt hat (zum Erkunden des echten Formats).
CREATE TABLE unparsed_events (
    id          INTEGER PRIMARY KEY,
    board_id    INTEGER REFERENCES boards(id) ON DELETE SET NULL,
    kind        TEXT NOT NULL,
    url         TEXT NOT NULL,
    reason      TEXT NOT NULL,
    raw_json    TEXT NOT NULL,
    received_at TEXT NOT NULL
);
