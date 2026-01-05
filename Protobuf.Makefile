# Copyright © 2025-2026 Apple Inc. and the container-builder-shim project authors.
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

ifeq ($(shell command -v git 2>/dev/null),)
  ROOT_DIR := $(CURDIR)
else
  ROOT_DIR := $(shell git rev-parse --show-toplevel 2>/dev/null || echo $(CURDIR))
endif

LOCAL_DIR  ?= $(ROOT_DIR)/.local
LOCALBIN   ?= $(LOCAL_DIR)/bin
$(shell mkdir -p $(LOCALBIN) 2>/dev/null)

PROTOC_VERSION             ?= 26.1
PROTOC_GEN_GO_VERSION      ?= v1.36.6
PROTOC_GEN_GO_GRPC_VERSION ?= v1.2

.PHONY: proto-all go-protos install-go-protoc clean-proto-tools

install-go-protoc: $(PROTOC_GEN_GO_GRPC) $(PROTOC_GEN_GO)

ifeq ($(shell uname -s),Darwin)
  PROTOC_ZIP := protoc-$(PROTOC_VERSION)-osx-universal_binary.zip
else
  PROTOC_ZIP := protoc-$(PROTOC_VERSION)-linux-x86_64.zip
endif

PROTOC_DIR := $(LOCALBIN)/protoc@$(PROTOC_VERSION)
PROTOC     := $(PROTOC_DIR)/protoc

$(PROTOC):
	@echo "Installing protoc $(PROTOC_VERSION)…"
	@mkdir -p $(PROTOC_DIR)
	@curl -sSL -o $(PROTOC_ZIP) \
	  https://github.com/protocolbuffers/protobuf/releases/download/v$(PROTOC_VERSION)/$(PROTOC_ZIP)
	@unzip -q -jo $(PROTOC_ZIP) bin/protoc -d $(PROTOC_DIR)
	@unzip -q -o  $(PROTOC_ZIP) 'include/*' -d $(PROTOC_DIR)
	@rm -f $(PROTOC_ZIP)

PROTOC_GEN_GO := $(LOCALBIN)/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)/protoc-gen-go
$(PROTOC_GEN_GO):
	@echo "Installing protoc-gen-go $(PROTOC_GEN_GO_VERSION)…"
	@GOBIN=$(dir $@) go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)

PROTOC_GEN_GO_GRPC := $(LOCALBIN)/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)/protoc-gen-go-grpc
$(PROTOC_GEN_GO_GRPC):
	@echo "Installing protoc-gen-go-grpc $(PROTOC_GEN_GO_GRPC_VERSION)…"
	@GOBIN=$(dir $@) go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)

GO_PROTO_SRC := pkg/api/Builder.proto

go-protos: $(PROTOC) $(PROTOC_GEN_GO) $(PROTOC_GEN_GO_GRPC)
	@echo "Generating Go protobuf & gRPC code…"
	@$(PROTOC) $(GO_PROTO_SRC) \
	  --plugin=$(PROTOC_GEN_GO) \
	  --plugin=$(PROTOC_GEN_GO_GRPC) \
	  --proto_path=pkg/api \
	  --go-grpc_out=. \
	  --go_out=. \
	  -I.
	@"$(MAKE)" update-licenses

clean-proto-tools:
	@rm -rf $(LOCAL_DIR)/bin
	@echo "Removed $(LOCAL_DIR)/bin toolchains."

