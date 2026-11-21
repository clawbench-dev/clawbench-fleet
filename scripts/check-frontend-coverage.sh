#!/usr/bin/env bash
# check-frontend-coverage.sh — Run the web unit tests with coverage and enforce
# the same two-tier gate as the Go side.
#
# Tier 1 — Project Gate: current statements% >= baseline% - 1.5% tolerance
# Tier 2 — Diff Coverage: changed lines coverage >= 80% (strict)
#
# Baseline: .clawbench-ci/baseline/frontend-coverage-summary.json
#   (downloaded from main by CI; absent locally on a fresh checkout, in which
#    case Tier 1 is skipped and only the diff gate runs)
#
# Usage:
#   ./scripts/check-frontend-coverage.sh              # run tests + check
#   ./scripts/check-frontend-coverage.sh --skip-test   # use existing coverage
#
# Exit code: 0 = pass, 1 = fail

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
WEB_DIR="$ROOT_DIR/web"
COVERAGE_DIR="$WEB_DIR/coverage"
SUMMARY="$COVERAGE_DIR/coverage-summary.json"
FINAL="$COVERAGE_DIR/coverage-final.json"
BASELINE="$ROOT_DIR/.clawbench-ci/baseline/frontend-coverage-summary.json"

SKIP_TEST=false
for arg in "$@"; do
  case "$arg" in
    --skip-test) SKIP_TEST=true ;;
    --help|-h)
      echo "Usage: $0 [--skip-test] [--help]"
      exit 0 ;;
    *)
      echo "未知参数: $arg" >&2
      exit 1 ;;
  esac
done

if [ "$SKIP_TEST" = false ]; then
  echo "==> Running frontend tests with coverage..."
  (cd "$WEB_DIR" && npx vitest run --coverage \
      --coverage.reporter=text \
      --coverage.reporter=json-summary \
      --coverage.reporter=json)
  echo ""
fi

if [ ! -f "$SUMMARY" ]; then
  echo "ERROR: $SUMMARY not found. Run without --skip-test first." >&2
  exit 1
fi

# Detect merge-base for diff coverage.
MERGE_BASE=""
if git -C "$ROOT_DIR" rev-parse --verify origin/main &>/dev/null; then
  MERGE_BASE=$(git -C "$ROOT_DIR" merge-base HEAD origin/main 2>/dev/null || true)
fi
if [ -z "$MERGE_BASE" ] && git -C "$ROOT_DIR" rev-parse --verify main &>/dev/null; then
  MERGE_BASE=$(git -C "$ROOT_DIR" merge-base HEAD main 2>/dev/null || true)
fi
if [ -z "$MERGE_BASE" ]; then
  MERGE_BASE=$(git -C "$ROOT_DIR" rev-parse HEAD~1 2>/dev/null || true)
fi

python3 - "$SUMMARY" "$BASELINE" "$FINAL" "$MERGE_BASE" "$ROOT_DIR" <<'PYTHON_SCRIPT'
import json
import os
import re
import subprocess
import sys
from collections import defaultdict

summary_path, baseline_path, final_path, merge_base, root = sys.argv[1:6]

TIER1_TOLERANCE = 1.5
DIFF_THRESHOLD = 80.0

BOLD = "\033[1m"
RED = "\033[0;31m"
GREEN = "\033[0;32m"
YELLOW = "\033[0;33m"
CYAN = "\033[0;36m"
RESET = "\033[0m"


def pass_fail(passed):
    return f"{GREEN}PASS{RESET}" if passed else f"{RED}FAIL{RESET}"


def pct(entry):
    return float(entry.get("pct", 0) or 0)


def rel(path):
    path = os.path.abspath(path)
    try:
        return os.path.relpath(path, root)
    except ValueError:
        return path


current = json.load(open(summary_path))
current_total = current.get("total", {})
current_pct = pct(current_total.get("statements", {}))

tier1_pass = True
tier1_skipped = False

if not os.path.isfile(baseline_path):
    tier1_skipped = True
    print(f"{CYAN}ℹ Tier 1 (Frontend) SKIPPED — no baseline at .clawbench-ci/baseline/{RESET}")
else:
    baseline = json.load(open(baseline_path))
    base_pct = pct(baseline.get("total", {}).get("statements", {}))
    floor = max(base_pct - TIER1_TOLERANCE, 0.0)
    tier1_pass = current_pct >= floor
    print()
    print(f"{BOLD}╔══════════════════════════════════════════════════════════════════╗{RESET}")
    print(f"{BOLD}║          Frontend Coverage Gate — Tier 1: Project              ║{RESET}")
    print(f"{BOLD}║  Rule: statements% >= baseline% - {TIER1_TOLERANCE}%                        ║{RESET}")
    print(f"{BOLD}╚══════════════════════════════════════════════════════════════════╝{RESET}")
    print()
    print(f"{'Statements':<20} {'Base%':>8} {'Curr%':>8} {'Floor':>8}  Status")
    print(f"{'─'*20} {'─'*8} {'─'*8} {'─'*8}  {'─'*8}")
    print(f"{'total':<20} {base_pct:>7.1f}% {current_pct:>7.1f}% {floor:>7.1f}%  {pass_fail(tier1_pass)}")
    print()
    if tier1_pass:
        print(f"{GREEN}{BOLD}✓ Tier 1 (Frontend) PASSED{RESET}")
    else:
        print(f"{RED}{BOLD}✗ Tier 1 (Frontend) FAILED{RESET}")

tier2_pass = True
tier2_skipped = False

if not merge_base or not os.path.isfile(final_path):
    tier2_skipped = True
    print(f"\n{CYAN}ℹ Tier 2 (Frontend diff) SKIPPED — no merge-base or coverage-final.json{RESET}")
else:
    diff = subprocess.run(
        ["git", "-C", root, "diff", "--unified=0", merge_base],
        capture_output=True, text=True,
    ).stdout

    changed = defaultdict(set)
    current_file = None
    for line in diff.split("\n"):
        if line.startswith("+++ b/"):
            current_file = line[6:].strip()
        elif line.startswith("@@"):
            m = re.search(r'\+(\d+)(?:,(\d+))?', line)
            if m and current_file:
                start = int(m.group(1))
                count = int(m.group(2)) if m.group(2) else 1
                for ln in range(start, start + count):
                    changed[current_file].add(ln)

    final = json.load(open(final_path))
    by_rel = {rel(path): data for path, data in final.items()}

    stats = {}
    for path, lines in changed.items():
        if not path.startswith("web/src/"):
            continue
        if not path.endswith((".ts", ".vue", ".js")):
            continue
        if path.endswith(".test.ts"):
            continue
        data = by_rel.get(path)
        if data is None:
            continue
        total = covered = 0
        for stmt_id, loc in data.get("statementMap", {}).items():
            if loc.get("start", {}).get("line") in lines:
                total += 1
                if data.get("s", {}).get(stmt_id, 0) > 0:
                    covered += 1
        if total:
            stats[path] = (covered, total)

    if not stats:
        tier2_skipped = True
        print(f"\n{CYAN}ℹ Tier 2 (Frontend diff) SKIPPED — no changed source lines with coverage data{RESET}")
    else:
        print()
        print(f"{BOLD}╔══════════════════════════════════════════════════════════════════╗{RESET}")
        print(f"{BOLD}║       Frontend Coverage Gate — Tier 2: Diff Coverage           ║{RESET}")
        print(f"{BOLD}║  Rule: changed lines coverage >= {DIFF_THRESHOLD}%                        ║{RESET}")
        print(f"{BOLD}╚══════════════════════════════════════════════════════════════════╝{RESET}")
        print()
        print(f"{'File':<50} {'Covered':>8} {'Total':>8} {'Diff%':>8}  Status")
        print(f"{'─'*50} {'─'*8} {'─'*8} {'─'*8}  {'─'*8}")
        total_all = covered_all = 0
        for path in sorted(stats):
            covered, total = stats[path]
            p = covered / total * 100
            total_all += total
            covered_all += covered
            ok = p >= DIFF_THRESHOLD
            if not ok:
                tier2_pass = False
            print(f"{path:<50} {covered:>8} {total:>8} {p:>7.1f}%  {pass_fail(ok)}")
        overall = covered_all / total_all * 100 if total_all else 100.0
        overall_ok = overall >= DIFF_THRESHOLD
        if not overall_ok:
            tier2_pass = False
        print(f"{'─'*50} {'─'*8} {'─'*8} {'─'*8}  {'─'*8}")
        print(f"{BOLD}{'TOTAL':<50} {covered_all:>8} {total_all:>8} {overall:>7.1f}%  {pass_fail(overall_ok)}{RESET}")
        print()
        if tier2_pass:
            print(f"{GREEN}{BOLD}✓ Tier 2 (Frontend diff) PASSED{RESET}")
        else:
            print(f"{RED}{BOLD}✗ Tier 2 (Frontend diff) FAILED{RESET}")

results = []
if not tier1_skipped:
    results.append(tier1_pass)
if not tier2_skipped:
    results.append(tier2_pass)

print()
if not results:
    print(f"{YELLOW}{BOLD}⚠ Frontend coverage gate — no checks could run{RESET}")
    sys.exit(0)

if all(results):
    print(f"{GREEN}{BOLD}✓ Frontend coverage gate PASSED{RESET}")
    sys.exit(0)

print(f"{RED}{BOLD}✗ Frontend coverage gate FAILED{RESET}")
sys.exit(1)
PYTHON_SCRIPT
