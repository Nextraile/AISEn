#!/bin/sh
# Startup guard: prove the bind-mounted data directory is writable by the UID
# this container runs as, and explain the fix in plain words if it is not.
#
# Two paths matter, and both are checked:
#   - the ChromaDB directory (CHROMA_DB_PATH) - Chroma writes its SQLite file here
#   - its parent (/app/data) - `ingest.py` writes history.json / documents.json
#     next to it, and `/ready` reads them.
#
# Checking only the DB directory was not enough: on a fresh checkout `./data`
# does not exist, so Docker creates the bind mount owned by root. The DB
# directory then does not exist yet either, the old check short-circuited, and
# the container died later with an opaque "Permission denied (os error 13)".
set -eu

DB_DIR=${CHROMA_DB_PATH:-/app/data/vector_db}
DATA_DIR=$(dirname "$DB_DIR")
RUN_UID=$(id -u)

explain_and_exit() {
    TARGET=$1
    DIR_UID=$(stat -c '%u' "$TARGET" 2>/dev/null || echo 'unknown')
    echo "ERROR: the data directory is not writable by this container." >&2
    echo "" >&2
    echo "  Directory : $TARGET (owned by UID $DIR_UID)" >&2
    echo "  Container : running as UID $RUN_UID (APP_UID build arg)" >&2
    echo "" >&2
    echo "ChromaDB needs write access just to open its SQLite database, and the" >&2
    echo "ingest summary is written next to it, so this would fail at startup with" >&2
    echo "an opaque 'Permission denied' or 'attempt to write a readonly database'." >&2
    echo "" >&2
    echo "This means the bind-mounted data directory is owned by another user." >&2
    echo "A fresh clone should not hit this: data/.gitkeep keeps the directory in" >&2
    echo "git, so it is created as your own user. It only goes wrong if the" >&2
    echo "directory was deleted and Docker recreated it as root. Fix that with:" >&2
    echo "  Linux/WSL : sudo chown -R \$(id -u):\$(id -g) data" >&2
    echo "              cd backend && APP_UID=\$(id -u) docker compose -f docker/docker-compose.yml --env-file .env up -d --build --wait" >&2
    echo "  PowerShell: \$env:APP_UID='$(id -u 2>/dev/null || echo 1000)'" >&2
    echo "              cd backend; docker compose -f docker/docker-compose.yml --env-file .env up -d --build --wait" >&2
    echo "" >&2
    echo "Stopgap only: chmod -R a+rwX \"$TARGET\" on the host." >&2
    exit 1
}

[ -d "$DATA_DIR" ] || mkdir -p "$DATA_DIR" 2>/dev/null || true
[ -w "$DATA_DIR" ] || explain_and_exit "$DATA_DIR"

# Create the DB directory if a fresh checkout has none, so the first ingest and
# a direct start both find it writable.
[ -d "$DB_DIR" ] || mkdir -p "$DB_DIR" 2>/dev/null || true
[ -w "$DB_DIR" ] || explain_and_exit "$DB_DIR"

exec "$@"
