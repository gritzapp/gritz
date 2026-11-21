#!/usr/bin/env bash
#
# Publishes a docs build to the gh-pages branch as a single orphan commit, so
# the branch only ever holds the current site and no history.
#
#   deploy-gh-pages.sh root DIR MESSAGE [PR...]
#     Replaces the branch root with DIR, keeping CNAME (managed by GitHub from
#     the Pages settings) and the previews under pr/<n>/ of the given PRs.
#     Every other preview is removed.
#
#   deploy-gh-pages.sh preview DIR MESSAGE PR
#     Replaces pr/<PR>/ with DIR and leaves the rest of the branch alone.
#
# Docs Pages and Docs Preview deploy concurrently. Each attempt starts from the
# current gh-pages commit and pushes with --force-with-lease against it, so a
# push that raced another deploy fails instead of overwriting it, and is
# retried on top of the new commit.
#
# Run from a checkout whose origin can be pushed to (actions/checkout's token).

set -euo pipefail

mode=$1
src=$(realpath "$2")
message=$3
shift 3

case $mode in
root) ;;
preview) [[ $# -eq 1 ]] || { echo "preview takes exactly one PR" >&2; exit 2; } ;;
*) echo "unknown mode: $mode" >&2; exit 2 ;;
esac

export GIT_AUTHOR_NAME='github-actions[bot]'
export GIT_AUTHOR_EMAIL='41898282+github-actions[bot]@users.noreply.github.com'
export GIT_COMMITTER_NAME=$GIT_AUTHOR_NAME
export GIT_COMMITTER_EMAIL=$GIT_AUTHOR_EMAIL

worktree=$(mktemp -d)
trap 'git worktree remove --force "$worktree" 2>/dev/null || true' EXIT

for attempt in $(seq 1 10); do
	git fetch --quiet --depth=1 origin gh-pages
	base=$(git rev-parse FETCH_HEAD)

	git worktree remove --force "$worktree" 2>/dev/null || true
	git worktree add --quiet --detach "$worktree" "$base"

	case $mode in
	root)
		find "$worktree" -mindepth 1 -maxdepth 1 \
			! -name .git ! -name CNAME ! -name pr -exec rm -rf {} +
		cp -a "$src/." "$worktree/"
		if [[ -d $worktree/pr ]]; then
			for dir in "$worktree"/pr/*/; do
				n=$(basename "$dir")
				[[ " $* " == *" $n "* ]] || rm -rf "$dir"
			done
		fi
		;;
	preview)
		rm -rf "$worktree/pr/$1"
		mkdir -p "$worktree/pr"
		cp -a "$src" "$worktree/pr/$1"
		;;
	esac

	git -C "$worktree" add --all
	tree=$(git -C "$worktree" write-tree)
	if [[ $tree == "$(git rev-parse "$base^{tree}")" ]]; then
		echo "gh-pages is already up to date"
		exit 0
	fi

	commit=$(git commit-tree "$tree" -m "$message")
	if git push --force-with-lease="refs/heads/gh-pages:$base" origin "$commit:refs/heads/gh-pages"; then
		exit 0
	fi

	echo "gh-pages moved during attempt $attempt, retrying" >&2
	sleep $((attempt * 2))
done

echo "gave up deploying to gh-pages after $attempt attempts" >&2
exit 1
