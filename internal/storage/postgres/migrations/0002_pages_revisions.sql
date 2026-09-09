-- Existing page/section model only. Metadata keeps the existing JSON shape and
-- RFC3339Nano timestamps: PostgreSQL timestamptz alone truncates API version tokens.
CREATE TABLE public.pages (
    id TEXT PRIMARY KEY,
    parent_id TEXT REFERENCES public.pages(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    slug TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('page', 'section')),
    position INTEGER NOT NULL CHECK (position >= 0),
    pinned BOOLEAN NOT NULL DEFAULT false,
    metadata JSONB NOT NULL,
    content_markdown TEXT,
    current_revision_id TEXT,
    CHECK ((id = 'root' AND parent_id IS NULL AND kind = 'section') OR
           (id <> 'root' AND parent_id IS NOT NULL)),
    CHECK (id <> parent_id)
);
CREATE UNIQUE INDEX pages_sibling_slug ON public.pages(parent_id, lower(slug));
CREATE INDEX pages_child_order ON public.pages(parent_id, position, id);
CREATE INDEX pages_title ON public.pages(lower(title));
INSERT INTO public.pages(id, title, slug, kind, position, metadata)
VALUES ('root', 'root', 'root', 'section', 0,
    jsonb_build_object('createdAt', to_char(current_timestamp AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
                      'updatedAt', to_char(current_timestamp AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
                      'creatorId', 'system', 'lastAuthorId', 'system'));

CREATE TABLE public.revision_contents (
    page_id TEXT NOT NULL REFERENCES public.pages(id) ON DELETE CASCADE,
    content_hash TEXT NOT NULL,
    content_markdown TEXT NOT NULL,
    PRIMARY KEY (page_id, content_hash)
);
CREATE TABLE public.revision_asset_manifests (
    hash TEXT PRIMARY KEY,
    manifest JSONB NOT NULL
);
CREATE TABLE public.revisions (
    page_id TEXT NOT NULL REFERENCES public.pages(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    sort_key TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    asset_manifest_hash TEXT NOT NULL REFERENCES public.revision_asset_manifests(hash),
    metadata JSONB NOT NULL,
    PRIMARY KEY (page_id, id),
    UNIQUE (page_id, sort_key),
    FOREIGN KEY (page_id, content_hash) REFERENCES public.revision_contents(page_id, content_hash)
);
CREATE INDEX revisions_history ON public.revisions(page_id, sort_key DESC);
CREATE INDEX revisions_content_refs ON public.revisions(page_id, content_hash);
CREATE INDEX revisions_manifest_refs ON public.revisions(asset_manifest_hash);
ALTER TABLE public.pages ADD CONSTRAINT pages_current_revision
    FOREIGN KEY (id, current_revision_id) REFERENCES public.revisions(page_id, id)
    DEFERRABLE INITIALLY DEFERRED;
