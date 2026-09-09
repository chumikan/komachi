-- No page/auth/revision tables yet: their existing semantics are migrated in
-- Phases 3-5. Extension creation is explicit, never a server-startup side effect.
CREATE EXTENSION IF NOT EXISTS pgroonga WITH SCHEMA public;
