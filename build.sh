#!/bin/sh
# Tests and builds in a container (Go pinned by digest). Binaries, SHA256SUMS and install.sh go to dist/.
# Usage: ./build.sh [test]
#        ./build.sh e2e URL   against a disposable appservice at URL (MATRIX_AI_ADMIN_TOKEN if its signup is closed)
set -eu
cd "$(dirname "$0")"
GO_IMAGE=docker.io/library/golang@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb # 1.27-alpine
run() { podman run --rm -v "$PWD:/src:Z" -w /src -e CGO_ENABLED=0 -e GOFLAGS=-trimpath "$@"; }

if [ "${1:-}" = e2e ]; then
  [ $# -eq 2 ] || { echo "usage: $0 e2e URL" >&2; exit 2; }
  run --network host -e MATRIX_AI_E2E_BASE="$2" -e MATRIX_AI_ADMIN_TOKEN "$GO_IMAGE" \
    go test -count 1 -run TestE2E -v ./cmd/matrix-for-ai-cli
  exit
fi
run "$GO_IMAGE" sh -c 'go vet ./... && go test ./...'
[ "${1:-}" = test ] && exit 0
rm -rf dist
run "$GO_IMAGE" sh -c '
for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os=${t%/*}; arch=${t#*/}; ext=; [ "$os" = windows ] && ext=.exe
  GOOS=$os GOARCH=$arch go build -ldflags="-s -w" -o "dist/matrix-for-ai-cli-$os-$arch$ext" ./cmd/matrix-for-ai-cli
done
cd dist && sha256sum matrix-for-ai-cli-* > SHA256SUMS'
cp install.sh dist/
ls -l dist
