-- Derived from current pages; never the canonical Markdown or revision store.
CREATE TABLE links (
    from_page_id TEXT NOT NULL,
    to_page_id TEXT,
    to_path TEXT NOT NULL,
    from_title TEXT,
    broken INTEGER NOT NULL DEFAULT 0 CHECK (broken IN (0, 1)),
    PRIMARY KEY (from_page_id, to_path)
);
CREATE INDEX idx_links_to_page_id ON links(to_page_id);
CREATE INDEX idx_links_to_path ON links(to_path);
CREATE INDEX idx_links_to_path_lower ON links(translate(to_path,'ABCDEFGHIJKLMNOPQRSTUVWXYZ','abcdefghijklmnopqrstuvwxyz'));
CREATE INDEX idx_links_broken ON links(broken);
CREATE TABLE page_tags (
    page_id TEXT NOT NULL,
    tag TEXT NOT NULL,
    PRIMARY KEY (page_id, tag)
);
CREATE INDEX idx_page_tags_tag ON page_tags(tag);
CREATE TABLE page_meta (
    page_id TEXT PRIMARY KEY,
    excerpt TEXT NOT NULL DEFAULT ''
);
CREATE TABLE page_properties (
    page_id TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    type TEXT NOT NULL DEFAULT 'text',
    PRIMARY KEY (page_id, key)
);
CREATE INDEX idx_page_properties_key ON page_properties(key);
CREATE INDEX idx_page_properties_key_value ON page_properties(key, value);
CREATE TABLE search_pages (
    page_id TEXT PRIMARY KEY,
    path TEXT NOT NULL,
    filepath TEXT NOT NULL,
    kind TEXT NOT NULL,
    title TEXT NOT NULL,
    headings TEXT NOT NULL,
    content TEXT NOT NULL
);
CREATE INDEX search_pages_full_text ON search_pages USING pgroonga ((ARRAY[title, headings, content, page_id]));
CREATE INDEX search_pages_filepath ON search_pages(filepath);
