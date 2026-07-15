#!/usr/bin/env bash
# Prunes session-driven git worktrees under .claude/worktrees/ (e.g. Claude
# Code remote-control worktrees) that are done with, so their disk, branch
# and e2e minikube profile don't pile up. Two ways a worktree counts as done:
#
#   1. Its branch has a merged/closed PR on GitHub -- reap worktree + branch.
#   2. It's "abandoned": no live `claude agents` session has it as cwd
#      anymore, and its last commit is older than STALE_DAYS. Sessions can be
#      abandoned mid-work (e.g. archived by the user) with no PR at all, so
#      this path never assumes the branch's work is safe to discard:
#        - the worktree checkout itself is always removable (branch history
#          survives in the ref regardless);
#        - the branch ref, and its e2e minikube profile via the
#          reference-transaction hook, are only deleted if the branch is
#          fully merged into origin/master (checked explicitly with
#          merge-base, since `git branch -d` alone checks against local HEAD,
#          which can lag origin) -- unmerged work is left for manual review
#          instead of being force-deleted;
#        - a lingering minikube profile for the branch is deleted directly
#          either way, since reclaiming host resources doesn't risk any git
#          history.
#   A worktree with uncommitted changes is never touched, in either path.
#
# Usage: hack/reap-worktrees.sh [--apply] [--stale-days N]
# Without --apply, only prints what would happen (dry run). STALE_DAYS
# defaults to 2.
set -euo pipefail

apply=false
stale_days=2
while [ $# -gt 0 ]; do
	case "$1" in
	--apply)
		apply=true
		shift
		;;
	--stale-days)
		stale_days="$2"
		shift 2
		;;
	*)
		echo "usage: $0 [--apply] [--stale-days N]" >&2
		exit 1
		;;
	esac
done

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"
worktrees_dir="$repo_root/.claude/worktrees"

command -v gh >/dev/null 2>&1 || {
	echo "gh CLI is required (to check PR state) but not found" >&2
	exit 1
}

command -v jq >/dev/null 2>&1 || {
	echo "jq is required (to parse Claude session state) but not found" >&2
	exit 1
}

if [ ! -d "$worktrees_dir" ]; then
	echo "No worktrees directory found at $worktrees_dir. Nothing to reap."
	exit 0
fi

git fetch origin master --quiet 2>/dev/null || true

# cwd of every still-running Claude Code session on this machine (interactive
# processes, plus background sessions not yet finished). A worktree whose
# path doesn't appear here has no session working on it anymore.
live_cwds=""
if command -v claude >/dev/null 2>&1; then
	live_cwds="$(claude agents --json --all 2>/dev/null \
		| jq -r '.[] | select(.kind != "background" or .state != "done") | .cwd' 2>/dev/null || true)"
fi

is_session_live() {
	local wt="$1"
	[ -z "$live_cwds" ] && return 1
	printf '%s\n' "$live_cwds" | grep -qxF "$wt"
}

hash_script="$repo_root/hack/e2e-hash.sh"

reap_minikube_profile() {
	local br="$1"
	command -v minikube >/dev/null 2>&1 || return 0
	[ -x "$hash_script" ] || return 0
	local branch_hash
	branch_hash="$("$hash_script" "$br")"
	local profiles
	profiles="$(minikube profile list -o json 2>/dev/null \
		| grep -o "\"Name\"[[:space:]]*:[[:space:]]*\"kstat-e2e-[a-z0-9-]*-${branch_hash}-[a-f0-9]\{8\}\"" \
		| cut -d'"' -f4 \
		| sort -u || true)"
	local profile
	for profile in $profiles; do
		echo "      deleting e2e minikube profile '$profile'"
		if $apply; then
			minikube delete -p "$profile" >/dev/null 2>&1 || true
		fi
	done
}

process_worktree() {
	local wt="$1" br="$2"

	if [ -n "$(git -C "$wt" status --porcelain 2>/dev/null)" ]; then
		echo "SKIP  $br ($wt): has uncommitted changes"
		return
	fi

	local pr_state
	pr_state="$(gh pr list --head "$br" --state all --json state,number --jq '.[0] | (.state // "NONE") + " #" + (.number // 0 | tostring)' 2>/dev/null || echo "NONE")"

	case "$pr_state" in
	MERGED\ *|CLOSED\ *)
		local upstream
		upstream="$(git -C "$wt" rev-parse --abbrev-ref --symbolic-full-name '@{u}' 2>/dev/null || true)"
		if [ -n "$upstream" ] && [ -n "$(git -C "$wt" log "$upstream.." --oneline 2>/dev/null)" ]; then
			echo "SKIP  $br ($wt): has unpushed commits"
			return
		fi
		if git merge-base --is-ancestor "$br" origin/master 2>/dev/null; then
			echo "REAP  $br ($wt): PR $pr_state (merged into origin/master)"
			reap_minikube_profile "$br"
			if $apply; then
				git worktree unlock "$wt" 2>/dev/null || true
				git worktree remove "$wt" || { echo "      ERROR removing worktree, leaving for manual review"; return; }
				# -D, not -d: already verified merged into origin/master above (-d checks
				# local HEAD instead, which can lag origin and wrongly refuse).
				git branch -D "$br" || echo "      ERROR deleting branch, leaving for manual review"
			fi
		else
			echo "PARTIAL-REAP  $br ($wt): PR $pr_state but has unmerged work -- removing worktree, keeping branch for manual review"
			reap_minikube_profile "$br"
			if $apply; then
				git worktree unlock "$wt" 2>/dev/null || true
				git worktree remove "$wt" || echo "      ERROR removing worktree, leaving for manual review"
			fi
		fi
		return
		;;
	esac

	if is_session_live "$wt"; then
		echo "SKIP  $br ($wt): session still running"
		return
	fi

	local last_commit_timestamp
	last_commit_timestamp="$(git -C "$wt" log -1 --format=%ct 2>/dev/null || true)"
	if [ -z "$last_commit_timestamp" ]; then
		echo "SKIP  $br ($wt): could not determine last commit timestamp"
		return
	fi

	local last_commit_age_days
	last_commit_age_days=$(( ($(date +%s) - last_commit_timestamp) / 86400 ))
	if [ "$last_commit_age_days" -lt "$stale_days" ]; then
		echo "SKIP  $br ($wt): no PR, session ended, but only ${last_commit_age_days}d old (< ${stale_days}d threshold)"
		return
	fi

	if git merge-base --is-ancestor "$br" origin/master 2>/dev/null; then
		echo "REAP  $br ($wt): abandoned (${last_commit_age_days}d), merged into origin/master"
		reap_minikube_profile "$br"
		if $apply; then
			git worktree unlock "$wt" 2>/dev/null || true
			git worktree remove "$wt" || { echo "      ERROR removing worktree, leaving for manual review"; return; }
			# -D, not -d: already verified merged into origin/master above (-d checks
			# local HEAD instead, which can lag origin and wrongly refuse).
			git branch -D "$br" || echo "      ERROR deleting branch, leaving for manual review"
		fi
	else
		echo "PARTIAL-REAP  $br ($wt): abandoned (${last_commit_age_days}d) but has unmerged work -- removing worktree, keeping branch for manual review"
		reap_minikube_profile "$br"
		if $apply; then
			git worktree unlock "$wt" 2>/dev/null || true
			git worktree remove "$wt" || echo "      ERROR removing worktree, leaving for manual review"
		fi
	fi
}

for wt in "$worktrees_dir"/*/; do
	[ -d "$wt" ] || continue
	wt="${wt%/}"
	branch="$(git -C "$wt" rev-parse --abbrev-ref HEAD 2>/dev/null || true)"
	[ -n "$branch" ] && [ "$branch" != "HEAD" ] || continue
	process_worktree "$wt" "$branch"
done
