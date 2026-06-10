#!/usr/bin/env bash
# gemini-subtree-split.sh — Split port/packages/gemini-extension/ to a distribution branch/tag.
#
# Usage:
#   scripts/gemini-subtree-split.sh [REF] [VERSION]
#
# Arguments:
#   REF      Git ref to split from (default: HEAD)
#   VERSION  Version string for the tag (default: derived from latest v* tag)
#            Example: "v0.55.6" → tag "gemini-extension-v0.55.6"
#
# What it does:
#   1. Runs `git subtree split` on port/packages/gemini-extension/ at REF.
#   2. Force-creates local branch gemini-extension-dist pointing at the split commit.
#   3. Creates annotated tag gemini-extension-<VERSION> on that commit.
#   4. Force-pushes branch and tag to origin.
#
# Safety:
#   - Uses --force flags so it is safe to re-run (idempotent for same REF).
#   - Does NOT push unless called without SKIP_PUSH=1 (useful for local testing).
#
# Local test (no push):
#   SKIP_PUSH=1 scripts/gemini-subtree-split.sh HEAD v0.55.100-test
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

REF="${1:-HEAD}"
VERSION="${2:-}"

# ── Validate version argument ────────────────────────────────────────────────
if [ -z "${VERSION:-}" ] || [ "$VERSION" = "v" ]; then
  echo "ERROR: version argument missing or invalid" >&2
  exit 1
fi

# Strip leading 'v' if caller passed it bare, then normalise tag name.
# Accepted inputs: "v0.55.6", "0.55.6", "v0.55.6-test"
# Resulting tag: "gemini-extension-v0.55.6" (always has the leading 'v')
if [[ "$VERSION" != v* ]]; then
  VERSION="v${VERSION}"
fi

DIST_BRANCH="gemini-extension-dist"
DIST_TAG="gemini-extension-${VERSION}"
SUBTREE_PREFIX="port/packages/gemini-extension"

echo "→ Splitting ${SUBTREE_PREFIX} from ref '${REF}' …"
SPLIT_COMMIT="$(git subtree split --prefix="${SUBTREE_PREFIX}" "${REF}")"
echo "  Split commit: ${SPLIT_COMMIT}"

echo "→ Creating/updating local branch '${DIST_BRANCH}' …"
git branch -f "${DIST_BRANCH}" "${SPLIT_COMMIT}"

echo "→ Creating annotated tag '${DIST_TAG}' …"
git tag -f -a "${DIST_TAG}" "${SPLIT_COMMIT}" \
  -m "Gemini extension ${VERSION} — subtree split from ${SUBTREE_PREFIX}"

echo "→ Verifying split tree contains gemini-extension.json …"
if ! git ls-tree "${DIST_BRANCH}" | grep -q "gemini-extension.json"; then
  echo "ERROR: gemini-extension.json not found at root of split branch '${DIST_BRANCH}'." >&2
  echo "  git ls-tree output:" >&2
  git ls-tree "${DIST_BRANCH}" >&2
  exit 1
fi

echo "  Tree looks good:"
git ls-tree "${DIST_BRANCH}"

if [[ "${SKIP_PUSH:-0}" == "1" ]]; then
  echo ""
  echo "SKIP_PUSH=1: skipping push to origin."
  echo "  Branch '${DIST_BRANCH}' created locally."
  echo "  Tag    '${DIST_TAG}' created locally."
  echo ""
  echo "To inspect:"
  echo "  git log --oneline ${DIST_BRANCH} -3"
  echo "  git ls-tree ${DIST_BRANCH} | head"
  exit 0
fi

echo "→ Pushing branch '${DIST_BRANCH}' to origin …"
git push origin "${DIST_BRANCH}" --force

echo "→ Pushing tag '${DIST_TAG}' to origin …"
git push origin "${DIST_TAG}" --force

echo ""
echo "Done. End-user install:"
echo "  gemini extensions install shakestzd/wipnote --ref ${DIST_TAG}"
