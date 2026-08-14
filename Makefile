.DEFAULT_GOAL := build

export NAME ?= fleeting-plugin-vmware
export VERSION := v$(shell cat VERSION)
export OUT_PATH ?= out
export CGO_ENABLED ?= 0

export CHECKSUMS_FILE_NAME := release.sha256
export CHECKSUMS_FILE := $(OUT_PATH)/$(CHECKSUMS_FILE_NAME)

REVISION := $(shell git rev-parse --short=8 HEAD || echo unknown)
REFERENCE := $(shell git show-ref | grep "$(REVISION)" | grep -v HEAD | awk '{print $$2}' | sed 's|refs/remotes/origin/||' | sed 's|refs/heads/||' | sort | head -n 1)
BUILT := $(shell date -u +%Y-%m-%dT%H:%M:%S%z)
PKG = $(shell go list .)

OS_ARCHS ?= darwin/amd64 darwin/arm64 \
			freebsd/amd64 freebsd/arm64 freebsd/386 freebsd/arm \
			linux/amd64 linux/arm64 linux/arm linux/s390x linux/ppc64le linux/386 \
			windows/amd64.exe windows/386.exe
GO_LDFLAGS ?= -X $(PKG).NAME=$(NAME) -X $(PKG).VERSION=$(VERSION) \
              -X $(PKG).REVISION=$(REVISION) -X $(PKG).BUILT=$(BUILT) \
              -X $(PKG).REFERENCE=$(REFERENCE) \
              -w -extldflags '-static'

build:
	@mkdir -p $(OUT_PATH)
	go build -a -ldflags "$(GO_LDFLAGS)" -o $(OUT_PATH)/$(NAME) ./cmd/$(NAME)/...

.PHONY: .mods
.mods:
	go mod download

TARGETS = $(foreach OSARCH,$(OS_ARCHS),${OUT_PATH}/$(NAME)-$(subst /,-,$(OSARCH)))

$(TARGETS): .mods
	@mkdir -p $(OUT_PATH)
	GOOS=$(firstword $(subst -, ,$(subst $(OUT_PATH)/$(NAME)-,,$@))) \
			 GOARCH=$(lastword $(subst .exe,,$(subst -, ,$(subst $(OUT_PATH)/$(NAME)-,,$@)))) \
			 go build -a -ldflags "$(GO_LDFLAGS)" -o $@ ./cmd/$(NAME)/...

MAKEFLAGS += -j$(shell nproc)
all:$(TARGETS)

.PHONY: test
test: .mods
	go test -v -timeout=30m ./...

.PHONY: real-vcenter-test
real-vcenter-test: .mods
	@mkdir -p $(OUT_PATH)
	go build -o $(OUT_PATH)/$(NAME)-realtest ./cmd/$(NAME)/...
	go test -v -timeout=15m ./test/real-vcenter/... \
		-plugin-binary-path=$(PWD)/$(OUT_PATH)/$(NAME)-realtest \
		-config-path=$(PWD)/test/real-vcenter/config.json \
		|| (rm -f $(OUT_PATH)/$(NAME)-realtest; exit 1)
	rm -f $(OUT_PATH)/$(NAME)-realtest

.PHONY: clean
clean:
	rm -fr $(OUT_PATH)

.PHONY: checksums
checksums:
	cd $(OUT_PATH) && sha256sum $(NAME)-* > $(CHECKSUMS_FILE_NAME)
