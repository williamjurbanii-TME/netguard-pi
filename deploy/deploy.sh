#!/usr/bin/env bash
# Build NetGuard for the Raspberry Pi and install or update it over SSH.
# macOS / Linux counterpart of deploy.ps1.
#
#   deploy/deploy.sh pi@192.168.1.2 --install       first time (also copies config.yaml)
#   deploy/deploy.sh pi@192.168.1.2                 update to the current code
#   deploy/deploy.sh pi@192.168.1.2 --push-config   update and replace the Pi's config.yaml
#   deploy/deploy.sh pi@192.168.1.2 --rollback      undo the last update
#   add --skip-tests to skip the test run
set -euo pipefail

TARGET="${1:?usage: deploy/deploy.sh user@pi-address [--install|--push-config|--rollback] [--skip-tests]}"
shift
MODE=update FLAG="" TESTS=1
for arg in "$@"; do
  case "$arg" in
    --install) MODE=install ;;
    --push-config) FLAG=--config ;;
    --rollback) MODE=rollback ;;
    --skip-tests) TESTS=0 ;;
    *) echo "unknown option: $arg" >&2; exit 2 ;;
  esac
done

cd "$(dirname "$0")/.."

if [ "$MODE" = rollback ]; then
  ssh -t "$TARGET" 'sudo bash /opt/netguard/install.sh rollback'
  exit
fi
if { [ "$MODE" = install ] || [ -n "$FLAG" ]; } && [ ! -f config.yaml ]; then
  echo "config.yaml not found. Copy config.example.yaml to config.yaml and edit it first." >&2
  exit 1
fi
if [ "$TESTS" = 1 ]; then
  echo "==> Running tests"
  go test ./...
fi

VERSION="$(date +%Y.%m.%d-%H%M)"
echo "==> Building NetGuard $VERSION for Raspberry Pi (linux/arm64)"
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o dist/netguard ./cmd/netguard

FILES=(dist/netguard deploy/install.sh deploy/netguard.service)
if [ "$MODE" = install ] || [ -n "$FLAG" ]; then
  FILES+=(config.yaml)
fi

echo "==> Uploading to $TARGET"
ssh "$TARGET" 'rm -rf /tmp/netguard-upload && mkdir -p /tmp/netguard-upload'
scp -q "${FILES[@]}" "$TARGET:/tmp/netguard-upload/"

echo "==> Running $MODE on the Pi"
ssh -t "$TARGET" "sudo bash /tmp/netguard-upload/install.sh $MODE $FLAG"
