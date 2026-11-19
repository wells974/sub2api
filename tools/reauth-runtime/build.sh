#!/usr/bin/env bash
set -euo pipefail
: "${VERSION:?set VERSION}"
: "${ARCH:?set ARCH}"
if ! [[ $VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+(\.[0-9]+)?(-[A-Za-z0-9.-]+)?$ ]]; then
  echo 'Invalid VERSION: expected X.Y.Z or X.Y.Z.N, optionally followed by -suffix' >&2
  exit 1
fi
[[ $ARCH == amd64 || $ARCH == arm64 ]]
if [[ -n ${RELEASE_SHA:-} ]]; then
  if [[ $(git rev-parse HEAD) != "$RELEASE_SHA" ]]; then
    echo 'Runtime checkout does not match RELEASE_SHA' >&2
    exit 1
  fi
fi
runtime_output=$(mktemp -d)
trap 'rm -rf "$runtime_output"' EXIT
docker buildx build --platform "linux/$ARCH" -f tools/reauth-runtime/Dockerfile \
  --output "type=local,dest=$runtime_output" .
if [[ -n ${RELEASE_SHA:-} ]]; then
  printf '{"version":"%s","commit":"%s"}\n' "$VERSION" "$RELEASE_SHA" > "$runtime_output/release-source.json"
fi
mkdir -p reauth-output
tar --dereference -czf "reauth-output/sub2api-reauth_${VERSION}_linux_${ARCH}.tar.gz" -C "$runtime_output" .
sha256sum "reauth-output/sub2api-reauth_${VERSION}_linux_${ARCH}.tar.gz"
