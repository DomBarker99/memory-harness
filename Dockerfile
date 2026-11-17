# memory-harness: a Go shell (kagent's bring-your-own contract) plus a TypeScript
# loop (the Pi SDK as the agent, Cognee as its long-term memory).
#
# Build for the cluster (amd64) and push; the Harness needs the pushed digest:
#   docker buildx build --platform linux/amd64 --provenance=false \
#     -t <registry>/memory-harness:dev --push .
#   docker buildx imagetools inspect <registry>/memory-harness:dev   # prints the sha256 digest
#
# Build for the Mac, to run a turn inside the image (test/run-turn-in-image.sh):
#   docker build -t memory-harness:scratch .

# Stage 1: compile the shell. Runs on the build machine, cross-compiles for the target.
FROM --platform=$BUILDPLATFORM golang:1.27 AS shell
ARG TARGETARCH
WORKDIR /src
COPY shell/ ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -o /harness-shell .

# Stage 2: install what the loop imports (the Pi SDK), exactly as locked. This
# has to happen here: a sandbox cannot download anything at run time.
FROM node:26-trixie-slim AS deps
WORKDIR /app/loop
COPY loop/package.json loop/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --omit=dev --ignore-scripts

# Stage 3: the image. Debian, because the agent's shell tool is only as useful
# as the programs it finds. ripgrep and fd are what Pi's grep and find tools
# run; without them Pi would try to download them.
FROM node:26-trixie-slim
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      bash ca-certificates curl fd-find git jq less procps ripgrep && \
    rm -rf /var/lib/apt/lists/*
# The same runtime user as kagent's own images. The shell writes the compiled
# config to /config; /data is the Actor's durable volume.
RUN groupadd -g 65532 kagent && \
    useradd -u 65532 -g kagent -m -d /home/kagent -s /bin/bash kagent && \
    mkdir -p /config /data && \
    chown -R 65532:65532 /config /data
COPY --from=shell /harness-shell /usr/local/bin/harness-shell
COPY --from=deps /app/loop/node_modules /app/loop/node_modules
# Node runs the .ts files directly (type stripping), so there is no build step.
COPY loop/package.json loop/*.ts /app/loop/
USER 65532:65532
# Pi must not look for updates, a model catalogue or telemetry: none of it is reachable.
ENV HOME=/home/kagent \
    PI_OFFLINE=1 \
    PI_SKIP_VERSION_CHECK=1 \
    PI_TELEMETRY=0
ENTRYPOINT ["/usr/local/bin/harness-shell"]
