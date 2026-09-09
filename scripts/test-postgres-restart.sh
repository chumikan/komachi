#!/bin/sh
# Run from the repository root. Only restarts the dedicated development service.
set -eu

compose() {
    docker compose -f compose.postgres.yml "$@"
}

compose up -d --wait postgres
compose run --build --rm database database migrate

migration_state() {
    compose exec -T postgres psql -X -U leafwiki -d leafwiki -At -v ON_ERROR_STOP=1 \
        -c "SELECT version, name, checksum, applied_at FROM public.schema_migrations ORDER BY version" \
        -c "SELECT extversion FROM pg_extension WHERE extname = 'pgroonga'"
}

# A uniquely named page and derived projections is removed on exit; never touch existing pages.
probe_id="phase5-restart-probe-$$"
cleanup() {
    compose exec -T postgres psql -X -U leafwiki -d leafwiki -v ON_ERROR_STOP=1 \
        -c "BEGIN; DELETE FROM links WHERE from_page_id='$probe_id'; DELETE FROM page_tags WHERE page_id='$probe_id'; DELETE FROM page_meta WHERE page_id='$probe_id'; DELETE FROM page_properties WHERE page_id='$probe_id'; DELETE FROM search_pages WHERE page_id='$probe_id'; DELETE FROM pages WHERE id='$probe_id'; COMMIT" >/dev/null
}
trap cleanup EXIT
compose exec -T postgres psql -X -U leafwiki -d leafwiki -v ON_ERROR_STOP=1 \
    -c "INSERT INTO pages(id,parent_id,title,slug,kind,position,metadata,content_markdown) VALUES('$probe_id','root','再起動確認','$probe_id','page',0,'{}','本文保持')" >/dev/null
compose exec -T postgres psql -X -U leafwiki -d leafwiki -v ON_ERROR_STOP=1 \
    -c "BEGIN; INSERT INTO search_pages VALUES('$probe_id','$probe_id','$probe_id.md','page','再起動確認','','本文保持'); INSERT INTO page_tags VALUES('$probe_id','restart'); INSERT INTO page_meta VALUES('$probe_id','本文保持'); INSERT INTO page_properties VALUES('$probe_id','probe','restart','text'); INSERT INTO links VALUES('$probe_id',NULL,'missing','再起動確認',1); COMMIT" >/dev/null
page_state() {
    compose exec -T postgres psql -X -U leafwiki -d leafwiki -At -v ON_ERROR_STOP=1 \
        -c "SELECT id,parent_id,title,slug,kind,content_markdown FROM pages WHERE id='$probe_id'" \
        -c "SELECT page_id FROM search_pages WHERE page_id='$probe_id' AND ARRAY[title,headings,content,page_id] &@~ '本文保持'" \
        -c "SELECT * FROM page_tags WHERE page_id='$probe_id'" \
        -c "SELECT * FROM page_meta WHERE page_id='$probe_id'" \
        -c "SELECT * FROM page_properties WHERE page_id='$probe_id'" \
        -c "SELECT * FROM links WHERE from_page_id='$probe_id'"
}
before_page=$(page_state)
test -n "$before_page"

before=$(migration_state)
test -n "$before"
compose restart postgres
compose up -d --wait postgres
after=$(migration_state)
test "$before_page" = "$(page_state)"
if [ "$before" != "$after" ]; then
    echo "Migration/extension state changed after PostgreSQL restart" >&2
    exit 1
fi
compose run --rm database database status
compose run --rm database database migrate
after_reapply=$(migration_state)
if [ "$before" != "$after_reapply" ]; then
    echo "Reapplying migrations changed committed migration state" >&2
    exit 1
fi
echo "PostgreSQL restart, page/derived index persistence and migration reapplication: passed"
