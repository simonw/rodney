#!/usr/bin/env bash
set -euo pipefail

# Compare origin (fork) against upstream (simonw/rodney).
# Fetches latest from both remotes and shows divergence.

UPSTREAM="simonw"
UPSTREAM_BRANCH="main"
ORIGIN_BRANCH="main"

# Ensure the upstream remote exists
if ! git remote get-url "$UPSTREAM" &>/dev/null; then
  echo "Adding upstream remote..."
  git remote add "$UPSTREAM" "https://github.com/simonw/rodney.git"
fi

echo "Fetching latest from $UPSTREAM and origin..."
git fetch "$UPSTREAM" --quiet
git fetch origin --quiet

echo ""
echo "=== Upstream ($UPSTREAM/$UPSTREAM_BRANCH) ==="
echo "Latest: $(git log --oneline -1 $UPSTREAM/$UPSTREAM_BRANCH)"
echo ""
echo "=== Origin (origin/$ORIGIN_BRANCH) ==="
echo "Latest: $(git log --oneline -1 origin/$ORIGIN_BRANCH)"

# Commits in origin not in upstream (our additions)
AHEAD=$(git rev-list --count "$UPSTREAM/$UPSTREAM_BRANCH..origin/$ORIGIN_BRANCH")
# Commits in upstream not in origin (upstream additions we're missing)
BEHIND=$(git rev-list --count "origin/$ORIGIN_BRANCH..$UPSTREAM/$UPSTREAM_BRANCH")

echo ""
echo "=== Divergence ==="
echo "Origin is $AHEAD commits ahead of upstream"
echo "Origin is $BEHIND commits behind upstream"

if [ "$AHEAD" -gt 0 ]; then
  echo ""
  echo "--- Commits in origin not in upstream ($AHEAD) ---"
  git log --oneline "$UPSTREAM/$UPSTREAM_BRANCH..origin/$ORIGIN_BRANCH"
fi

if [ "$BEHIND" -gt 0 ]; then
  echo ""
  echo "--- Commits in upstream not in origin ($BEHIND) ---"
  git log --oneline "origin/$ORIGIN_BRANCH..$UPSTREAM/$UPSTREAM_BRANCH"
fi

# File-level diff summary
echo ""
echo "=== File diff summary (origin vs upstream) ==="
git diff --stat "$UPSTREAM/$UPSTREAM_BRANCH..origin/$ORIGIN_BRANCH"

if [ "$BEHIND" -gt 0 ]; then
  echo ""
  echo "NOTE: Run 'git merge $UPSTREAM/$UPSTREAM_BRANCH' to pull in upstream changes."
fi
