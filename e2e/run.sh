#!/usr/bin/env bash

set -euo pipefail

current_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$current_dir/.." && pwd)"
app_port="${E2E_PORT:-8085}"
app_url="${E2E_BASE_URL:-http://localhost:${app_port}}"
run_mode="${E2E_RUN_MODE:-docker}"
server_pid=""
server_log=""
server_bin=""
local_data_dir=""
docker_data_volume=""
postgres_container=""
postgres_volume=""
postgres_network=""

print_runner_diagnostics() {
  echo "--- E2E runtime diagnostics ---"

  if [ "$run_mode" = "docker" ]; then
    if docker ps -a --format '{{.Names}}' | grep -q '^wiki-e2e-tests$'; then
      echo "--- docker logs (last 200 lines) ---"
      docker logs --tail 200 wiki-e2e-tests 2>&1 || true
    else
      echo "No Docker container logs available."
    fi
    return
  fi

  if [ -n "$server_log" ] && [ -f "$server_log" ]; then
    echo "--- local server log (last 200 lines) ---"
    tail -n 200 "$server_log" || true
  else
    echo "No local server log available."
  fi
}

build_frontend_for_local_e2e() {
  if [ "${E2E_SKIP_UI_BUILD:-0}" = "1" ]; then
    echo "⚡ Skipping UI build for local E2E run..."
  else
    echo "🔨 Building frontend for local E2E run..."
    (
      cd "$repo_root/ui/leafwiki-ui"
      npm run build
    )
  fi

  if [ ! -f "$repo_root/ui/leafwiki-ui/dist/index.html" ]; then
    echo "❌ Frontend build output is missing at ui/leafwiki-ui/dist/index.html"
    exit 1
  fi

  rm -rf "$repo_root/internal/http/dist"
  mkdir -p "$repo_root/internal/http/dist"
  cp -R "$repo_root/ui/leafwiki-ui/dist/." "$repo_root/internal/http/dist/"
  touch "$repo_root/internal/http/dist/.gitkeep"
}

start_docker() {
  echo "🟢 Starting Docker container..."
  docker build \
    --build-arg DISABLE_REFRESH_TOKEN_RATE_LIMIT=true \
    -t wiki-e2e-tests \
    "$repo_root"

  if docker ps -a --format '{{.Names}}' | grep -q '^wiki-e2e-tests$'; then
    echo "⚠️ Removing existing container..."
    docker rm -f wiki-e2e-tests >/dev/null 2>&1 || true
  fi

  docker_data_volume="wiki-e2e-tests-data-${RANDOM}${RANDOM}"
  docker volume create "$docker_data_volume" >/dev/null

  postgres_container="leafwiki-e2e-pg-${RANDOM}${RANDOM}"
  postgres_volume="${postgres_container}-data"
  postgres_network="${postgres_container}-network"
  docker network create "$postgres_network" >/dev/null
  docker volume create "$postgres_volume" >/dev/null
  docker run -d --name "$postgres_container" --network "$postgres_network" \
    --network-alias postgres -v "$postgres_volume":/var/lib/postgresql/data \
    -e POSTGRES_USER=leafwiki -e POSTGRES_PASSWORD=e2e-only-password \
    -e POSTGRES_DB=leafwiki \
    groonga/pgroonga:4.0.8-alpine-17@sha256:e61f2b18d62287326878b83ea3bbde4f02efe9859da3285400e0b7af36c1e976 >/dev/null
  local ready=false
  for ((attempt=0; attempt<60; attempt++)); do
    if docker exec "$postgres_container" pg_isready -h 127.0.0.1 -U leafwiki -d leafwiki >/dev/null 2>&1; then
      ready=true
      break
    fi
    sleep 1
  done
  if [ "$ready" != true ]; then echo "PostgreSQL did not become ready" >&2; return 1; fi
  local database_url='postgres://leafwiki:e2e-only-password@postgres:5432/leafwiki?sslmode=disable'
  docker run --rm --network "$postgres_network" -e LEAFWIKI_DATABASE_URL="$database_url" \
    wiki-e2e-tests database migrate

  docker run -d \
    --network "$postgres_network" -e LEAFWIKI_DATABASE_URL="$database_url" \
    -p "$app_port:8080" \
    --name wiki-e2e-tests \
    -v "$docker_data_volume":/app/data \
    wiki-e2e-tests \
    --allow-insecure=true \
    --enable-revision=true \
    --enable-link-refactor=true \
    --revision-coalesce-window=0 \
    --jwt-secret=e2e-tests-secret \
    --totp-encryption-key=e2e-tests-totp-encryption-key-32 \
    --admin-password=admine2epassword

  echo "✅ Container started on $app_url"
}

stop_docker() {
  echo "🛑 Stopping Docker container..."
  docker stop wiki-e2e-tests >/dev/null 2>&1 || true
  docker rm wiki-e2e-tests >/dev/null 2>&1 || true
  docker rmi wiki-e2e-tests >/dev/null 2>&1 || true
  if [ -n "$postgres_container" ]; then docker rm -f "$postgres_container" >/dev/null 2>&1 || true; fi
  if [ -n "$postgres_volume" ]; then docker volume rm "$postgres_volume" >/dev/null 2>&1 || true; fi
  if [ -n "$postgres_network" ]; then docker network rm "$postgres_network" >/dev/null 2>&1 || true; fi
  if [ -n "$docker_data_volume" ]; then
    docker volume rm "$docker_data_volume" >/dev/null 2>&1 || true
  fi
}

start_local() {
  : "${LEAFWIKI_DATABASE_URL:?Set LEAFWIKI_DATABASE_URL to a dedicated migrated E2E database}"
  echo "🟢 Starting local LeafWiki process..."
  build_frontend_for_local_e2e

  local_data_dir="$(mktemp -d /tmp/leafwiki-e2e-data.XXXXXX)"
  server_log="$(mktemp /tmp/leafwiki-e2e-server.XXXXXX.log)"
  server_bin="$(mktemp /tmp/leafwiki-e2e-bin.XXXXXX)"

  # Build the binary first, then run it directly in the background. Using
  # `go run` here would make `$!` the PID of the `go run` wrapper, not of the
  # compiled server it execs as a child — so `stop_local`'s `kill` would leave
  # the real server orphaned, holding the port after the script exits.
  echo "🔨 Building leafwiki binary for local E2E run..."
  (
    cd "$repo_root"
    go build \
      -ldflags="-X github.com/perber/wiki/internal/http.EmbedFrontend=true -X github.com/perber/wiki/internal/http.Environment=production -X github.com/perber/wiki/internal/wiki/auth.DisableRefreshTokenRateLimit=true" \
      -o "$server_bin" \
      ./cmd/leafwiki
  )

  (
    cd "$repo_root"
    exec "$server_bin" \
      --host 127.0.0.1 \
      --port "$app_port" \
      --data-dir "$local_data_dir" \
      --allow-insecure=true \
      --enable-revision=true \
      --enable-link-refactor=true \
      --revision-coalesce-window=0 \
      --jwt-secret=e2e-tests-secret \
      --totp-encryption-key=e2e-tests-totp-encryption-key-32 \
      --admin-password=admine2epassword
  ) >"$server_log" 2>&1 &

  server_pid=$!
  echo "✅ Local process started on $app_url (pid $server_pid)"
}

stop_local() {
  echo "🛑 Stopping local LeafWiki process..."
  if [ -n "$server_pid" ] && kill -0 "$server_pid" >/dev/null 2>&1; then
    kill "$server_pid" >/dev/null 2>&1 || true
    wait "$server_pid" >/dev/null 2>&1 || true
  fi
  if [ -n "$local_data_dir" ] && [ -d "$local_data_dir" ]; then
    rm -rf "$local_data_dir"
  fi

  if [ -n "$server_log" ] && [ -f "$server_log" ]; then
    rm -f "$server_log"
  fi

  if [ -n "$server_bin" ] && [ -f "$server_bin" ]; then
    rm -f "$server_bin"
  fi
}

stop_runner() {
  if [ "$run_mode" = "docker" ]; then
    stop_docker
  else
    stop_local
  fi
}

cleanup_runner() {
  local exit_code=$?

  if [ "$exit_code" -ne 0 ]; then
    print_runner_diagnostics
  fi

  stop_runner
  exit "$exit_code"
}

run_playwright_tests() {
  echo "Running Playwright tests..."
  (
    cd "$current_dir"
    local reporter="${E2E_PLAYWRIGHT_REPORTER:-line}"
    local workers="${E2E_PLAYWRIGHT_WORKERS:-1}"

    if command -v stdbuf >/dev/null 2>&1; then
      E2E_BASE_URL="$app_url" \
      E2E_ADMIN_USER="${E2E_ADMIN_USER:-admin}" \
      E2E_ADMIN_PASSWORD="${E2E_ADMIN_PASSWORD:-admine2epassword}" \
      PLAYWRIGHT_FORCE_TTY=1 \
      stdbuf -oL -eL npx playwright test --workers="$workers" --reporter="$reporter" "$@"
    else
      E2E_BASE_URL="$app_url" \
      E2E_ADMIN_USER="${E2E_ADMIN_USER:-admin}" \
      E2E_ADMIN_PASSWORD="${E2E_ADMIN_PASSWORD:-admine2epassword}" \
      PLAYWRIGHT_FORCE_TTY=1 \
      npx playwright test --workers="$workers" --reporter="$reporter" "$@"
    fi
  )
}

wait_until_reachable() {
  local max_attempts=60
  local attempt=0

  until curl -s "$app_url" >/dev/null; do
    printf '.'
    sleep 2
    attempt=$((attempt + 1))
    if [ "$attempt" -ge "$max_attempts" ]; then
      echo
      echo "❌ LeafWiki is not reachable after 2 minutes."
      if [ "$run_mode" = "local" ] && [ -f "$server_log" ]; then
        echo "--- local server log ---"
        tail -n 200 "$server_log" || true
      fi
      exit 1
    fi
  done

  echo
  echo "✅ LeafWiki is reachable."
}

if nc -z localhost "$app_port" >/dev/null 2>&1; then
  if docker ps -a --format '{{.Names}}' | grep -q '^wiki-e2e-tests$'; then
    echo "⚠️ Port $app_port already in use by an existing E2E container – restarting it..."
    stop_docker
  else
    echo "❌ Port $app_port is already in use. Stop the existing process or choose another E2E_PORT."
    exit 1
  fi
fi

trap cleanup_runner EXIT

if [ "$run_mode" = "docker" ]; then
  start_docker
else
  start_local
fi

canonical_fingerprint() {
  # Compare each field independently; diagnostics contain only field names and hashes.
  docker exec "$postgres_container" psql -X -U leafwiki -d leafwiki -At -v ON_ERROR_STOP=1 \
    -c "WITH rows AS (SELECT 'pages' AS tbl, id, to_jsonb(p) - 'current_revision_id' AS data FROM pages p UNION ALL SELECT 'users', id, to_jsonb(u) FROM users u UNION ALL SELECT 'sessions', id, to_jsonb(s) FROM sessions s) SELECT tbl, md5(id), field.key, md5(field.value::text) FROM rows CROSS JOIN LATERAL jsonb_each(data) field ORDER BY tbl, id, field.key" \
    -c "SELECT version,checksum FROM schema_migrations ORDER BY version"
}

verify_docker_persistence() {
  [ "$run_mode" = docker ] || return 0
  local before after
  # Test-only snapshots stay inside this disposable database. Startup may add
  # baseline history for metadata-only edits, as in the filesystem contract.
  docker exec -i "$postgres_container" psql -X -U leafwiki -d leafwiki -v ON_ERROR_STOP=1 >/dev/null <<'SQL'
CREATE SCHEMA e2e_verification;
CREATE TABLE e2e_verification.pages AS SELECT id,current_revision_id FROM pages;
CREATE TABLE e2e_verification.revisions AS SELECT * FROM revisions;
SQL
  before=$(canonical_fingerprint)
  docker restart "$postgres_container" >/dev/null
  local ready=false
  for ((attempt=0; attempt<60; attempt++)); do
    if docker exec "$postgres_container" pg_isready -h 127.0.0.1 -U leafwiki -d leafwiki >/dev/null 2>&1; then ready=true; break; fi
    sleep 1
  done
  [ "$ready" = true ] || return 1
  # Existing app must reconnect before it is itself restarted.
  curl --fail --silent --retry 20 --retry-all-errors --retry-delay 1 "$app_url/api/health" >/dev/null
  docker restart wiki-e2e-tests >/dev/null
  wait_until_reachable
  curl --fail --silent --retry 20 --retry-all-errors --retry-delay 1 "$app_url/api/health" >/dev/null
  after=$(canonical_fingerprint)
  if [ "$before" != "$after" ]; then
    echo "Restart changed canonical data (field hashes only):" >&2
    diff <(printf '%s\n' "$before") <(printf '%s\n' "$after") >&2 || true
    return 1
  fi
  docker exec -i "$postgres_container" psql -X -U leafwiki -d leafwiki -v ON_ERROR_STOP=1 <<'SQL'
DO $$
BEGIN
  IF EXISTS (SELECT * FROM e2e_verification.revisions EXCEPT SELECT * FROM revisions) THEN
    RAISE EXCEPTION 'Restart changed or removed an existing revision';
  END IF;
  IF EXISTS (
    SELECT 1 FROM revisions r
    LEFT JOIN e2e_verification.revisions old ON old.page_id=r.page_id AND old.id=r.id
    LEFT JOIN e2e_verification.pages p ON p.id=r.page_id
    LEFT JOIN e2e_verification.revisions prior ON prior.page_id=p.id AND prior.id=p.current_revision_id
    WHERE old.id IS NULL AND (
      r.metadata->>'summary' IS DISTINCT FROM 'baseline' OR
      r.metadata->>'type' IS DISTINCT FROM 'content_update' OR
      r.metadata->>'author_id' IS DISTINCT FROM 'system' OR
      prior.id IS NULL OR r.content_hash IS DISTINCT FROM prior.content_hash OR
      r.metadata->>'extra_frontmatter_hash' IS NOT DISTINCT FROM prior.metadata->>'extra_frontmatter_hash'
    )
  ) THEN
    RAISE EXCEPTION 'Restart created a revision other than the metadata-only baseline contract';
  END IF;
  IF EXISTS (
    SELECT 1 FROM pages p JOIN e2e_verification.pages old ON old.id=p.id
    WHERE p.current_revision_id IS DISTINCT FROM old.current_revision_id AND NOT EXISTS (
      SELECT 1 FROM revisions r WHERE r.page_id=p.id AND r.id=p.current_revision_id
      AND NOT EXISTS (SELECT 1 FROM e2e_verification.revisions b WHERE b.page_id=r.page_id AND b.id=r.id)
    )
  ) THEN
    RAISE EXCEPTION 'Restart changed a pointer without creating its baseline revision';
  END IF;
END $$;
SELECT count(*) AS verified_metadata_baselines FROM revisions r
WHERE NOT EXISTS (SELECT 1 FROM e2e_verification.revisions b WHERE b.page_id=r.page_id AND b.id=r.id);
DROP SCHEMA e2e_verification CASCADE;
SQL
  # Once the baseline is captured, a second startup must preserve history and
  # pointers too. Nothing is excluded from this full row fingerprint.
  local stable_before stable_after
  stable_before=$(docker exec "$postgres_container" psql -X -U leafwiki -d leafwiki -At -v ON_ERROR_STOP=1 \
    -c "SELECT md5(jsonb_agg(to_jsonb(p) ORDER BY id)::text) FROM pages p" \
    -c "SELECT md5(jsonb_agg(to_jsonb(r) ORDER BY page_id,id)::text) FROM revisions r")
  docker restart wiki-e2e-tests >/dev/null
  wait_until_reachable
  stable_after=$(docker exec "$postgres_container" psql -X -U leafwiki -d leafwiki -At -v ON_ERROR_STOP=1 \
    -c "SELECT md5(jsonb_agg(to_jsonb(p) ORDER BY id)::text) FROM pages p" \
    -c "SELECT md5(jsonb_agg(to_jsonb(r) ORDER BY page_id,id)::text) FROM revisions r")
  [ "$stable_before" = "$stable_after" ] || { echo "Second startup changed canonical history" >&2; return 1; }
  [ "$before" = "$(canonical_fingerprint)" ] || { echo "Second startup changed canonical data" >&2; return 1; }
  local sqlite_files
  sqlite_files=$(docker exec wiki-e2e-tests find /app/data -type f -name '*.db*')
  [ -z "$sqlite_files" ] || { echo "Normal runtime created SQLite files" >&2; return 1; }
  echo "Fresh PostgreSQL/app restart, reconnect, canonical persistence, no SQLite files: PASS"
}

wait_until_reachable
run_playwright_tests "$@"
verify_docker_persistence
