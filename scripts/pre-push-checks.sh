#!/usr/bin/env bash
#
# pre-push-checks.sh — 提交 PR 前的本地预检，复刻 CI 流水线的全部检查
#
# 避免 push 后才发现远端 CI 报错，本地先跑一遍。
#
# 用法：
#   ./scripts/pre-push-checks.sh                  # 全量检查
#   ./scripts/pre-push-checks.sh --skip-coverage  # 跳过覆盖率门槛（仅 lint + test + build + typecheck）
#   ./scripts/pre-push-checks.sh --fix            # 对 golangci-lint 传 --fix 自动修复
#
# 退出码：0 = 全部通过，1 = 有检查失败

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT_DIR"

# 默认参数
SKIP_COVERAGE=false
FIX_MODE=""
FAILED=()
PASSED=()

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BOLD='\033[1m'
NC='\033[0m'

for arg in "$@"; do
    case "$arg" in
        --skip-coverage)  SKIP_COVERAGE=true ;;
        --fix)            FIX_MODE="--fix" ;;
        --help|-h)
            echo "用法: $0 [--skip-coverage] [--fix] [--help]"
            echo ""
            echo "  --skip-coverage   跳过覆盖率门槛检查"
            echo "  --fix             golangci-lint --fix 自动修复"
            echo "  --help            显示帮助"
            exit 0
            ;;
        *)
            echo "未知参数: $arg"
            exit 1
            ;;
    esac
done

run_check() {
    local name="$1"
    shift
    echo -e "${BOLD}━━━ $name ━━━${NC}"
    echo "  命令: $*"
    echo ""
    if "$@"; then
        echo -e "${GREEN}✅ $name 通过${NC}"
        PASSED+=("$name")
    else
        echo -e "${RED}❌ $name 失败${NC}"
        FAILED+=("$name")
    fi
    echo ""
}

# ─── 1. golangci-lint ───
if [ -n "$FIX_MODE" ]; then
    run_check "Lint (Go)" "$SCRIPT_DIR/lint-go.sh" "$FIX_MODE"
else
    run_check "Lint (Go)" "$SCRIPT_DIR/lint-go.sh"
fi

# ─── 2. Go test（单元测试）───
run_check "Test (Go)" go test -timeout 20m ./...

# ─── 3. 集成测试编译检查 ───
# 集成套件在 //go:build integration 后面，go test ./... 不会编译它们。
# go vet 会做类型检查，能拦住重构后仍引用旧符号的集成测试文件。
run_check "Compile (Integration)" go vet -tags integration ./...

# ─── 4. Go 集成测试 ───
run_check "Test (Integration)" go test -tags integration -timeout 20m ./...

# ─── 5. Go build ───
run_check "Build (Go)" go build ./...

# ─── 6. 前端 typecheck + build ───
if command -v npm >/dev/null 2>&1 && [ -f "$ROOT_DIR/web/package.json" ]; then
    if [ ! -d "$ROOT_DIR/web/node_modules" ]; then
        echo -e "${YELLOW}⚠️  web/node_modules 不存在，先运行 npm ci...${NC}"
        (cd "$ROOT_DIR/web" && npm ci)
        echo ""
    fi
    run_check "Lint (Frontend)" bash -c "cd '$ROOT_DIR/web' && npx eslint src/"
    run_check "Build (Frontend, includes typecheck)" bash -c "cd '$ROOT_DIR/web' && npm run build"
else
    echo -e "${YELLOW}⚠️  跳过 Frontend build（未找到 npm/web）${NC}"
    echo ""
fi

# ─── 7. 前端单元测试 ───
if command -v npm >/dev/null 2>&1 && [ -f "$ROOT_DIR/web/package.json" ]; then
    run_check "Test (Frontend)" bash -c "cd '$ROOT_DIR/web' && npm test"
else
    echo -e "${YELLOW}⚠️  跳过 Frontend test（未找到 npm/web）${NC}"
    echo ""
fi

# ─── 8. Go coverage gate ───
if [ "$SKIP_COVERAGE" = false ]; then
    run_check "Coverage Gate (Go)" "$SCRIPT_DIR/check-go-coverage.sh"
else
    echo -e "${YELLOW}⚠️  跳过 Go 覆盖率门槛（--skip-coverage）${NC}"
    echo ""
fi

# ─── 9. Frontend coverage gate ───
if [ "$SKIP_COVERAGE" = false ]; then
    if command -v npm >/dev/null 2>&1 && [ -f "$ROOT_DIR/web/package.json" ]; then
        run_check "Coverage Gate (Frontend)" "$SCRIPT_DIR/check-frontend-coverage.sh"
    else
        echo -e "${YELLOW}⚠️  跳过 Frontend 覆盖率门槛（未找到 npm/web）${NC}"
        echo ""
    fi
else
    echo -e "${YELLOW}⚠️  跳过 Frontend 覆盖率门槛（--skip-coverage）${NC}"
    echo ""
fi

# ─── 汇总 ───
echo -e "${BOLD}════════════════════════════════════════${NC}"
echo -e "${BOLD}预检结果汇总${NC}"
echo -e "${BOLD}════════════════════════════════════════${NC}"
echo ""

if [ ${#PASSED[@]} -gt 0 ]; then
    echo -e "${GREEN}通过 (${#PASSED[@]}):${NC}"
    for name in "${PASSED[@]}"; do
        echo -e "  ${GREEN}✅ $name${NC}"
    done
fi

if [ ${#FAILED[@]} -gt 0 ]; then
    echo ""
    echo -e "${RED}失败 (${#FAILED[@]}):${NC}"
    for name in "${FAILED[@]}"; do
        echo -e "  ${RED}❌ $name${NC}"
    done
    echo ""
    echo -e "${RED}${BOLD}预检未通过，请修复后再 push/创建 PR${NC}"
    exit 1
else
    echo ""
    echo -e "${GREEN}${BOLD}全部预检通过，可以安全 push/创建 PR${NC}"
    exit 0
fi
