-- 002_settings.sql — runtime-editable settings (e.g. the VWorld tile key)
-- that an admin can change from the web UI without restarting the server
-- or redeploying config. See internal/settings.
--
-- Not part of the original SPEC.md data model (§4); added per explicit user
-- request to let an admin enter their own VWorld key from the admin page.
-- SPEC §0 rule 4 note: this is additive (a new table, no existing behavior
-- removed) and env vars remain the fallback when no row exists here, so it
-- doesn't contradict §11.1's config-validation requirements.
CREATE TABLE setting (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_by TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
