-- Initial schema: speakers and their voice prints, intent rules, and the
-- utterance history that doubles as the debugging tool.

CREATE TABLE speakers (
  id         INTEGER PRIMARY KEY,
  name       TEXT UNIQUE NOT NULL,
  role       TEXT NOT NULL DEFAULT 'member',   -- 'owner' | 'member' | 'kid'
  centroid   BLOB NOT NULL,                    -- 192 x float32, little endian
  threshold  REAL,                             -- optional per-speaker override
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE enrollments (
  id         INTEGER PRIMARY KEY,
  speaker_id INTEGER NOT NULL REFERENCES speakers(id) ON DELETE CASCADE,
  embedding  BLOB NOT NULL,
  duration_s REAL,
  label      TEXT,                             -- which prompt produced it
  created_at TEXT NOT NULL
);

CREATE INDEX idx_enrollments_speaker ON enrollments(speaker_id);

CREATE TABLE utterances (
  id                INTEGER PRIMARY KEY,
  ts                TEXT NOT NULL,
  satellite_id      TEXT,
  speaker           TEXT,
  confidence        REAL,
  runner_up         TEXT,
  runner_up_score   REAL,
  transcript        TEXT,
  intent            TEXT,
  reply             TEXT,
  allowed           INTEGER NOT NULL DEFAULT 1,
  error             TEXT,
  audio_path        TEXT,
  latency_ms_total  INTEGER,
  latency_ms_stt    INTEGER,
  latency_ms_spk    INTEGER,
  latency_ms_intent INTEGER,
  latency_ms_tts    INTEGER
);

CREATE INDEX idx_utterances_ts ON utterances(ts DESC);
CREATE INDEX idx_utterances_speaker ON utterances(speaker);

CREATE TABLE intents (
  id             INTEGER PRIMARY KEY,
  name           TEXT UNIQUE NOT NULL,
  enabled        INTEGER NOT NULL DEFAULT 1,
  match_type     TEXT NOT NULL,                 -- 'exact' | 'contains' | 'regex'
  patterns       TEXT NOT NULL,                 -- JSON array of strings
  min_role       TEXT NOT NULL DEFAULT 'member',
  handler        TEXT NOT NULL,                 -- 'reply' | 'webhook' | 'llm'
  handler_config TEXT NOT NULL DEFAULT '{}',    -- JSON
  priority       INTEGER NOT NULL DEFAULT 100,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);

CREATE INDEX idx_intents_priority ON intents(priority, id);

-- Runtime-editable settings from the web UI. Values are JSON.
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
