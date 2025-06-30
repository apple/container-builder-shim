#!/usr/bin/env bash 
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

GOLANGCI_LINTER_VERSION=v2.1.6

install_golangci() {
  ci_install=false
  if [[ -x $(which golangci-lint) ]]; then
      local version=$(golangci-lint version --short)
      if [[ "${version}" == "${GOLANGCI_LINTER_VERSION#v}" ]]; then
          ci_install=false
        else
          ci_install=true
      fi
    else
      ci_install=true
  fi
  if [[ "$ci_install" == true ]]; then
      curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/cc3567e3127d8530afb69be1b7bd20ba9ebcc7c1/install.sh \
        | sh -s -- -b $(go env GOPATH)/bin "${GOLANGCI_LINTER_VERSION}"
  fi
}

function main() {
  case $1 in
    --golangci)
      install_golangci
      ;;
    *)
      echo "unknown command" >&2
      show_help
      exit 1
      ;;
  esac
}

main "$@"
