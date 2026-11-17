#!/bin/sh
# The same as run-turn.sh, but the loop runs inside the built image, the way it
# will on the cluster: as uid 65532, with only /data kept between turns, and
# with only what the image itself contains (no downloads at run time).
#
#   docker build -t memory-harness:scratch .
#   test/run-turn-in-image.sh <session> 'message'
#
# The stub model and Cognee still run on the Mac; the container reaches them
# through host.docker.internal. Environment as for run-turn.sh, with COGNEE_URL
# written for the Mac (http://127.0.0.1:18000) — it is rewritten for the container.
#
# This does not start the shell: that needs kagent's controller. It proves the
# image's loop half only.
here=$(cd "$(dirname "$0")/.." && pwd)
[ -n "$2" ] || { echo "usage: test/run-turn-in-image.sh <session> 'message'" >&2; exit 2; }

data="${TEST_DATA:-/tmp/memory-harness-data}/$1"
mkdir -p "$data"
# config.json as kagent would write it, with the stub model's address as seen from a container.
sed 's|127.0.0.1|host.docker.internal|' "$here/test/config.json" > "$data/../config-in-image.json"

input=$(node -e 'console.log(JSON.stringify({contextId: process.argv[1], taskId: "t" + Date.now(), text: process.argv[2]}))' "$1" "$2")

printf '%s\n' "$input" | docker run --rm -i \
  --entrypoint node \
  -v "$data:/data" \
  -v "$data/../config-in-image.json:/config/config.json:ro" \
  -e COGNEE_URL="$(printf '%s' "$COGNEE_URL" | sed 's|127.0.0.1|host.docker.internal|; s|localhost|host.docker.internal|')" \
  -e COGNEE_API_KEY -e COGNEE_DATASET \
  "${IMAGE:-memory-harness:scratch}" /app/loop/loop.ts |
  if [ -n "$VERBOSE" ]; then cat; else grep -v '^{"type":"text",'; fi
