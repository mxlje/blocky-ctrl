version := `git describe --tags --always --dirty 2>/dev/null || echo dev`
ldflags := "-s -w -X main.version=" + version
compose := "docker --context orbstack compose -f dev/compose.yml"

# Build for this machine into bin/
build:
    CGO_ENABLED=0 go build -trimpath -ldflags '{{ldflags}}' -o bin/blocky-ctrl .

# Cross-compile release binaries into dist/
dist:
    #!/usr/bin/env bash
    set -euo pipefail
    for t in linux/amd64 linux/arm64 linux/arm darwin/arm64 darwin/amd64; do
      os=${t%/*}; arch=${t#*/}
      CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags '{{ldflags}}' -o dist/blocky-ctrl-$os-$arch .
    done
    ls -lh dist

test:
    go vet ./...
    go test -race ./...

# Two blocky instances sharing state through Redis (APIs on :14101 and :14102, DNS on :15301 and :15302)
dev-up:
    {{compose}} up -d

dev-down:
    {{compose}} down

# Run the UI against the dev stack on http://127.0.0.1:18090
dev: build
    ./bin/blocky-ctrl --config dev/blocky-ctrl.yml --bind 127.0.0.1 --port 18090

# Send some DNS traffic through both dev instances
traffic:
    for p in 15301 15302; do for d in example.com github.com doubleclick.net ads.example.com x.tracker.test nas.lan malware.example.org; do \
      dig @127.0.0.1 -p $p +short +time=2 +tries=1 $d >/dev/null; done; done
