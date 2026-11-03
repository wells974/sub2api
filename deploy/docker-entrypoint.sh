#!/bin/sh
set -e

# Fix data directory permissions when running as root.
# Docker named volumes / host bind-mounts may be owned by root,
# preventing the non-root sub2api user from writing files.
if [ "$(id -u)" = "0" ]; then
    mkdir -p /app/data
    # Use || true to avoid failure on read-only mounted files (e.g. config.yaml:ro)
    chown -R sub2api:sub2api /app/data 2>/dev/null || true
    # Re-invoke this script as sub2api so the flag-detection below
    # also runs under the correct user.
    exec su-exec sub2api "$0" "$@"
fi

# Compatibility: if the first arg looks like a flag (e.g. --help),
# prepend the default binary so it behaves the same as the old
# ENTRYPOINT ["/app/sub2api"] style.
if [ "${1#-}" != "$1" ]; then
    set -- /app/sub2api "$@"
fi

# Opt in to web updates that survive container recreation. Keep the entire
# executable directory on the data volume so the updater's atomic rename and
# backup operations stay on one filesystem. Never overwrite an installed update
# on startup; disable this option explicitly to run a different image binary.
if [ "${SUB2API_PERSIST_UPDATES:-false}" = "true" ] && [ "${1:-}" = "/app/sub2api" ]; then
    runtime_dir=/app/data/runtime
    runtime_binary=$runtime_dir/sub2api
    mkdir -p "$runtime_dir"
    if [ ! -e "$runtime_binary" ]; then
        runtime_tmp=$(mktemp "$runtime_dir/.bootstrap-XXXXXX")
        trap 'rm -f "$runtime_tmp"' EXIT
        cp /app/sub2api "$runtime_tmp"
        chmod 0755 "$runtime_tmp"
        mv "$runtime_tmp" "$runtime_binary"
        trap - EXIT
    fi
    if [ ! -f "$runtime_binary" ] || [ ! -x "$runtime_binary" ]; then
        echo "Persisted update binary is not executable: $runtime_binary" >&2
        exit 1
    fi
    shift
    set -- "$runtime_binary" "$@"
fi

exec "$@"
