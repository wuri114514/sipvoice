GO      ?= go
VERSION ?= $(shell sed -n 's/^var version = "\(.*\)"/\1/p' cmd/voicebridge/main.go)
LDFLAGS := -s -w
DIST    := dist

.PHONY: all build test vet fmt clean release linux-amd64 linux-arm64 package

all: build

## 本机架构
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/voicebridge ./cmd/voicebridge

## 交叉编译
linux-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" \
		-o $(DIST)/voicebridge-linux-amd64 ./cmd/voicebridge

linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" \
		-o $(DIST)/voicebridge-linux-arm64 ./cmd/voicebridge

## 发布包（两个架构）
release: linux-amd64 linux-arm64
	VERSION=$(VERSION) bash ./scripts/package.sh amd64
	VERSION=$(VERSION) bash ./scripts/package.sh arm64
	@echo "发布包在 $(DIST)/release"

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

clean:
	rm -rf $(DIST) ; rm -rf release
