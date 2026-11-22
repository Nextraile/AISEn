#!/bin/sh
# Startup guard: prove the bind-mounted ChromaDB directory is writable by the
# UID this container runs as, and explain the fix in plain words if it is not
set -eu

DB_DIR=${CHROMA_DB_PATH:-/app/data/vector_db}
RUN_UID=$(id -u)

if [ -d "$DB_DIR" ] && [ ! -w "$DB_DIR" ]; then
    DIR_UID=$(stat -c '%u' "$DB_DIR" 2>/dev/null || echo 'unknown')
    echo "ERROR: the ChromaDB directory is not writable by this container." >&2
    echo "" >&2
    echo "  Directory : $DB_DIR (owned by UID $DIR_UID)" >&2
    echo "  Container : running as UID $RUN_UID (APP_UID build arg)" >&2
    echo "" >&2
    echo "ChromaDB needs write access just to open its SQLite database, so this" >&2
    echo "would fail at startup with 'attempt to write a readonly database'." >&2
    echo "" >&2
    echo "Fix: rebuild passing the UID that owns the directory on the host." >&2
    echo "  Linux/WSL : APP_UID=\$(id -u) docker compose up -d --build --wait" >&2
    echo "  PowerShell: \$env:APP_UID=$DIR_UID; docker compose up -d --build --wait" >&2
    echo "" >&2
    echo "Stopgap only: chmod -R a+rwX \"$DB_DIR\" on the host." >&2
    exit 1
fi

exec "$@"
