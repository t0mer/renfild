-- A couple of rules so a fresh install answers something before the owner has
-- written any of their own. Both are deliberately harmless.

INSERT INTO intents (name, enabled, match_type, patterns, min_role, handler, handler_config, priority, created_at, updated_at)
VALUES
  ('greeting', 1, 'contains',
   '["hello","hi there","good morning","שלום"]',
   'any', 'reply',
   '{"template":"Hello {{.Speaker}}."}',
   10, datetime('now'), datetime('now')),
  ('what time is it', 1, 'contains',
   '["what time is it","what''s the time","מה השעה"]',
   'any', 'reply',
   '{"template":"It is {{.Now.Format \"15:04\"}}."}',
   20, datetime('now'), datetime('now'));
