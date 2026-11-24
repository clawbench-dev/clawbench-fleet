#!/usr/bin/env node
// Install git hooks for local development.
// Run via `npm run prepare` from web/ (triggered by `npm install`).

import { writeFileSync, chmodSync, existsSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// The script lives in <repo>/scripts; resolve the repository from its own
// location so it works from web/ (npm run prepare) or from the root.
const repoRoot = join(dirname(fileURLToPath(import.meta.url)), '..');
const hooksDir = join(repoRoot, '.git', 'hooks');

if (!existsSync(hooksDir)) {
    console.log('Not a git repo — skipping hook installation');
    process.exit(0);
}

// ── pre-commit ────────────────────────────────────────────────────────────
// Lints only the staged Go and frontend files, so a commit stays fast. The
// full gate (tests, integration tests, coverage) runs in pre-push and CI.
const preCommitPath = join(hooksDir, 'pre-commit');
const preCommitScript = `#!/bin/sh
# Pre-commit hook: golangci-lint on staged Go files, ESLint on staged frontend files
# Installed by: npm run prepare (scripts/install-hooks.mjs)

STATUS=0

STAGED_GO=$(git diff --cached --name-only --diff-filter=ACM -- '*.go')
if [ -n "$STAGED_GO" ]; then
    if command -v golangci-lint >/dev/null 2>&1; then
        # Turn staged file paths into unique package directories.
        PKGS=$(echo "$STAGED_GO" | xargs -n1 dirname | sort -u | sed 's|^|./|' | sed 's|$|/...|')
        echo "Running golangci-lint on staged packages..."
        # Resolved at run time so a worktree lints its own files.
        ROOT=$(git rev-parse --show-toplevel) || exit 1
        cd "$ROOT" || exit 1
        golangci-lint run $PKGS || STATUS=1
    else
        echo "golangci-lint not installed; run ./scripts/lint-go.sh once to install it."
    fi
fi

STAGED_WEB=$(git diff --cached --name-only --diff-filter=ACM -- 'web/src/**/*.ts' 'web/src/**/*.vue')
if [ -n "$STAGED_WEB" ] && [ -x "${repoRoot}/web/node_modules/.bin/eslint" ]; then
    STAGED_WEB_REL=$(echo "$STAGED_WEB" | sed 's|^web/||')
    echo "Running ESLint on staged frontend files..."
    ROOT=$(git rev-parse --show-toplevel) || exit 1
    (cd "$ROOT/web" && npx eslint $STAGED_WEB_REL) || STATUS=1
fi

if [ $STATUS -ne 0 ]; then
    echo ""
    echo "Lint found errors. Fix them before committing."
    echo "   Run: ./scripts/lint-go.sh --fix && cd web && npm run lint:fix"
    exit 1
fi
`;

// ── pre-push ──────────────────────────────────────────────────────────────
// The full local gate, mirroring CI: lint, unit tests, integration tests,
// build and the two coverage gates. It is the last chance to see a red
// pipeline before teammates do.
const prePushPath = join(hooksDir, 'pre-push');

const prePushScript = `#!/bin/sh
# Pre-push hook: run the same checks as CI before pushing
# Installed by: npm run prepare (scripts/install-hooks.mjs)
# Skip once (not for merging): git push --no-verify

# Resolve the checkout at run time rather than baking in ${repoRoot}: the same
# hooks directory serves every worktree, and a hard-coded path would make a push
# from a worktree check the main checkout's code instead of its own.
ROOT=$(git rev-parse --show-toplevel) || exit 1
cd "$ROOT" || exit 1
exec ./scripts/pre-push-checks.sh
`;

writeFileSync(preCommitPath, preCommitScript);
chmodSync(preCommitPath, 0o755);
writeFileSync(prePushPath, prePushScript);
chmodSync(prePushPath, 0o755);
console.log('Installed hooks: pre-commit (golangci-lint + ESLint), pre-push (full gate)');
