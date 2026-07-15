#!/usr/bin/env bash
# Shared 8-hex-char hash used to build/match e2e minikube profile names (see the
# E2E_* variables in the Makefile and hack/git-hooks/reference-transaction).
# Kept as its own script, alongside hack/e2e-branch-slug.sh, so both places
# compute it identically.
set -euo pipefail

printf '%s' "${1:-}" | (sha1sum 2>/dev/null || shasum) | cut -c1-8
