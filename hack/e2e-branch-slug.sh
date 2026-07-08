#!/usr/bin/env bash
# Sanitizes a git branch name into the readable prefix used for e2e minikube
# profile names (see the E2E_* variables in the Makefile and
# hack/git-hooks/reference-transaction). Shared so both places stay in sync.
set -euo pipefail

printf '%s' "$1" | tr 'A-Z' 'a-z' | tr -c 'a-z0-9' '-' | sed 's/-\{1,\}/-/g;s/^-//;s/-$//' | cut -c1-20 | sed 's/-$//'
