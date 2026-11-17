#!/bin/sh
# Run one turn of the loop on the Mac, with no kagent and no real model.
#
#   test/run-turn.sh <session> 'message'
#
# <session> is any name. It plays the kagent Session: turns with the same name
# share one data folder (their conversation), different names start empty and
# only share what Cognee remembers.
#
# Needs, already running:
#   the stub model      node test/stub-model.mts 18317 /tmp/model-requests.jsonl
#   a Cognee server     test/cognee.env has the settings and the docker command
# and in the environment:
#   COGNEE_URL          e.g. http://127.0.0.1:18000   (leave unset to run without memory)
#   COGNEE_API_KEY      the key of a Cognee agent user (test/cognee.env says how to make one)
#   COGNEE_DATASET      optional; defaults to the agent's name, memory-agent
#   TEST_DATA           where session folders go (default /tmp/memory-harness-data)
#
# Prints the loop's events. Text deltas are left out unless VERBOSE=1.
here=$(cd "$(dirname "$0")/.." && pwd)
[ -n "$2" ] || { echo "usage: test/run-turn.sh <session> 'message'" >&2; exit 2; }

export HARNESS_CONFIG="$here/test/config.json"
export HARNESS_DATA_DIR="${TEST_DATA:-/tmp/memory-harness-data}/$1"
export PI_OFFLINE=1 PI_SKIP_VERSION_CHECK=1 PI_TELEMETRY=0
mkdir -p "$HARNESS_DATA_DIR"

# The turn input, built with node so the message is quoted correctly.
input=$(node -e 'console.log(JSON.stringify({contextId: process.argv[1], taskId: "t" + Date.now(), text: process.argv[2]}))' "$1" "$2")

if [ -n "$VERBOSE" ]; then
  printf '%s\n' "$input" | node "$here/loop/loop.ts"
else
  printf '%s\n' "$input" | node "$here/loop/loop.ts" | grep -v '^{"type":"text",'
fi
