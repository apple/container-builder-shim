# Copyright © 2025 Apple Inc. and the container-builder-shim project authors. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#   https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Usage: run `make help` to view available targets.

BINARY_NAME ?= container-builder-shim
BUILD_DIR   ?= bin
PKG         := ./...

GO          ?= go
GIT_TAG     := $(shell git describe --tags --always --dirty)
GO_LDFLAGS  := -s -w -X main.VERSION=$(GIT_TAG)
GOFLAGS    ?= -ldflags="$(GO_LDFLAGS)"

GOLANGCI_LINT := $(shell command -v golangci-lint 2>/dev/null)

-include Protobuf.Makefile

.DEFAULT_GOAL := build

$(BUILD_DIR):
	@mkdir -p $@

.PHONY: help
help:
	@printf "\033[1mAvailable targets:\033[0m\n"
	@awk 'BEGIN{FS=":.*##"} /^[a-zA-Z_-]+:.*##/{printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST) | sort

.PHONY: build
build: $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) .

.PHONY: build-linux
build-linux: $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux .

.PHONY: fmt
fmt:	go-fmt update-licenses

.PHONY: go-fmt
go-fmt:
	$(GO) fmt $(PKG)

.PHONY: vet
vet:
	$(GO) vet $(PKG)

.PHONY: lint
lint:
ifndef GOLANGCI_LINT
	@echo "golangci-lint not found – skipping. Install from https://golangci-lint.run/welcome/install/ to enable." >&2
else
	$(GOLANGCI_LINT) run
endif

.PHONY: tidy
tidy:
	$(GO) mod tidy

.PHONY: deps
deps: tidy
	$(GO) mod download

TEST_FLAGS ?= -v

.PHONY: test
test:
	$(GO) test $(TEST_FLAGS) $(PKG)

.PHONY: test-race
test-race:
	$(GO) test -race $(TEST_FLAGS) $(PKG)

.PHONY: coverage
coverage:
	$(GO) test -coverprofile=coverage.out $(PKG)
	$(GO) tool cover -func=coverage.out

.PHONY: protos
protos: proto-all

.PHONY: generate
generate: protos
	$(GO) generate $(PKG)

.PHONY: update-licenses
update-licenses:
	@echo Updating license headers...
	@./scripts/ensure-hawkeye-exists.sh
	@.local/bin/hawkeye format --fail-if-unknown --fail-if-updated false

.PHONY: check-licenses
check-licenses:
	@echo Checking license headers existence in source files...
	@./scripts/ensure-hawkeye-exists.sh
	@.local/bin/hawkeye check --fail-if-unknown

.PHONY: image
image: build-linux
	container build -t $(BINARY_NAME):$(GIT_TAG) .

.PHONY: release
release: fmt vet lint test build-linux docker-image

.PHONY: clean
clean:
	$(GO) clean
	rm -rf $(BUILD_DIR) coverage.out

