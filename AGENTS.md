# AGENTS.md

给在本仓库工作的 AI 助手看的说明。人写文档看 README，这里只记「做了会踩坑」的事。

---

## 1. 同步本地 main：用 rebase，别用 reset

远端开启了 squash 合并。自己的 PR 合进去之后，本地那几个原始提交和远端那条压缩后的提交内容重复，`git status` 会显示「领先 4，落后 1」。

**正确做法：**

```bash
git fetch origin
git rebase origin/main
```

已经合进去的提交会被自动识别为「空提交」丢掉，本地历史自然对齐，没有丢东西。

**不要这么做：**

```bash
git reset --hard origin/main   # 会抹掉还没合并的本地提交
```

一旦用了 reset，被抹掉的提交除非还在 reflog 里，否则找不回来。真用错了，先 `git reflog` 找回。

---

## 2. pre-commit / pre-push 钩子带来的 `GIT_*` 环境变量

git 执行钩子时，会把 `GIT_DIR`、`GIT_WORK_TREE` 之类的环境变量传进钩子进程。钩子里的 `go test` 继承了这些变量，于是测试代码里任何 `git init`、`git rev-parse` 都会指向**主仓库**，而不是测试自己造的临时目录。

症状：

- 本地直接 `go test ./...` 全绿，一 push 就红
- 测试报 `Not a git repo — skipping hook installation`，或者把单元文件写进了主仓库

**规矩：测试里碰 git 之前，先把这些变量清掉。**

```go
for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY"} {
    t.Setenv(k, "")
}
```

同理，`git init` 之类的命令别直接调系统 git，用 `exec.Command` 显式给一份干净的环境变量（`internal/fleet/install_hooks_test.go` 里的 `initRepo` 就是这么写的）。

---

## 3. 仓库配置可能被带歪成 `core.bare = true`

同一个原因：钩子或脚本里跑 `git init`、`git rev-parse` 时继承了 `GIT_DIR`，git 会认为 `.git` 是某个裸仓库，顺手在 `.git/config` 里写一行 `bare = true`。

后果很隐蔽：工作区文件「消失」、`git status` 报「该操作必须在一个工作区中运行」、`git worktree list` 显示主仓库是 bare。**数据没丢，只是 git 不再认为这里有工作区。**

修复：

```bash
git config --unset core.bare     # 删掉那行
git reset --hard origin/main     # 把文件从对象库里放回来
```

发现这类状态时先查 `.git/config`，不要急着重建仓库。

---

## 4. 改动过 `scripts/install-hooks.mjs` 之后，重装钩子

钩子内容在安装时就被写进 `.git/hooks/` 了。改了脚本不重装，本地跑的还是旧的。

```bash
cd web && npm run prepare
```

钩子本身也有一条规矩：**不许把仓库绝对路径写死**。同一个 `.git/hooks` 被所有 worktree 共用，写死路径会导致在 worktree 里提交/推送时，检查的是主仓库那份代码。用 `git rev-parse --show-toplevel` 在运行时定位（`internal/fleet/install_hooks_test.go` 里有回归测试盯着）。

---

## 5. 提交前先跑一遍完整预检

CI 的检查项和 `scripts/pre-push-checks.sh` 是一套，本地先跑能省一轮来回：

```bash
./scripts/pre-push-checks.sh              # 全量
./scripts/pre-push-checks.sh --skip-coverage   # 只查 lint + 测试 + 构建
```

包含：golangci-lint、Go 单元测试、集成测试编译检查、集成测试、前端 ESLint、前端类型检查与构建、两端覆盖率门槛。

---

## 6. 覆盖率门槛是「改动行」算的，不是总量

新增/修改的后端代码，被测试覆盖的行要达到 80%（`scripts/check-go-coverage.sh` 的 Tier 2）。只写一个 happy path 通常不够，失败路径和边界要一起补。

前端覆盖率只统计 `src/api.ts`、`src/stores/**`、`src/utils.ts`，`.vue` 文件不在里面（见 `web/vite.config.ts`）。

---

## 7. 中文为主，但注释和测试名用英文

代码注释、测试函数名、提交信息里的标题行用英文；面向用户的错误提示、界面文案用中文。lint 带拼写检查（misspell），注释里别写英式拼写（`neighbour`、`initialises` 会被拦）。
