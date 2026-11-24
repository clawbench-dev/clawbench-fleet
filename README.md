# Clawbench Fleet

面向企业 IT 的 **Clawbench 实例管理平台**：在一台 Linux 服务器上批量创建 Linux 用户，为每个用户运行一个独立的 Clawbench 实例，并用一套 Web 界面统一管理这些实例。

设计文档：[docs/design.md](docs/design.md)

---

## 能做什么

| 能力 | 说明 |
|---|---|
| 批量建号 | 单个创建，或用 CSV 一次导入几十到几百个；导入前先预览重名、格式和端口占用 |
| 接管老实例 | 扫描本机已有 Linux 用户和 Clawbench 数据目录，勾选后把服务改成平台统一管理；并发处理并显示每个账号的进度，root 也纳入，但不能删除 |
| 实例运维 | 启动、停止、重启、修改端口、编辑显示名和备注、重置密码、修复、批量操作 |
| 状态与用量 | 实例存活、Clawbench 版本、CPU / 内存 / 进程数 / 磁盘占用，机器整体负载 |
| 日志 | 直接看 systemd journal 最近若干行，不用登服务器 |
| 备份与恢复 | 每天自动备份数据库和配置，可手动备份、一键恢复、下载 |
| 回收站 | 删除后整个家目录保留 30 天，可恢复账号、端口和数据；也可以立即强制删除，跳过保留期 |
| 版本升级 | 从 npm 直接拉取新版本（内网可上传二进制）→ 试点一个实例 → 批量升级 → 一键回滚，失败自动回退 |
| 平台自升级 | 在同一个页面里上传新版 `clawbench-fleet`，自动备份旧程序、替换并重启后台服务，不影响员工实例 |
| 审计 | 所有写操作留痕，保留 180 天，可按操作人、动作、租户、时间筛选 |
| 员工自助页 | 免登录的只读页面，员工查自己的实例地址和端口，看状态和公告，打不开可一键报障 |
| SSH 登录 | 新租户默认开 SSH，和网页登录用同一个密码；每个租户可单独关掉。平台只准备账号，不改服务器 sshd 配置 |
| 资源限制 | 每个实例用 systemd 限制 CPU、内存、进程数，默认值可改并一键下发到所有已有实例；磁盘可设硬上限，超了自动停实例、锁 SSH 并结束该账号的进程 |
| 批量运维 | 批量启停重启、批量改配额（CPU/内存/进程数/磁盘）、批量重置密码（新密码放在可下载的表格里） |
| 用量历史 | 每 5 分钟记一个点，详情页看最近 24 小时或 7 天的 CPU、内存、磁盘曲线 |

明确不做：员工门户（自助页只做地址查询，不能进实例）、网页终端、浏览员工文件、多机调度、模型密钥统一下发。

---

## 技术栈

- 后端：Go 1.26，单二进制内嵌前端，SQLite 存平台数据
- 前端：Vue 3 + TypeScript + Vite
- 系统集成：systemd（每个租户一个服务单元）、Linux 用户隔离

```
管理员浏览器 ──HTTP──> clawbench-fleet (root, :30000)
                         ├── SQLite  fleet.db（租户/端口/备份/审计/任务）
                         ├── systemctl · useradd · journalctl · cgroup
                         ├── /opt/clawbench-fleet/versions/<版本>/ + current 软链
                         └── /var/lib/clawbench-fleet/{backups,trash,logs}

员工浏览器 ──HTTPS──> IP:21000  clawbench (User=alice, systemd)
                      IP:21003  clawbench (User=bob,   systemd)
```

---

## 快速开始（开发模式）

不需要 root，也不需要 systemd。`--dev` 会用一套内存版系统命令模拟 useradd / systemctl，并为每个租户起一个假的 Clawbench 实例。

```bash
./build.sh                       # 构建前端 + 后端（前端会打进二进制）
./clawbench-fleet --dev          # 默认 127.0.0.1:30000，账号 admin / admin123
```

开发模式的数据放在 `./.fleet-dev`，删掉这个目录就等于重置。

想用 Vite 热更新改前端：

```bash
cd web && npm run dev            # http://127.0.0.1:5173，API 自动代理到 30000
```

---

## 生产部署

环境要求：Linux + systemd（推荐 Ubuntu 22.04+ / Debian 12+）、cgroup v2、root 权限、Node.js 22+（Clawbench 的 ACP 适配器要用）。

```bash
./build.sh
sudo ./deploy/install.sh
```

安装脚本会：安装二进制到 `/usr/local/bin`、生成 `/etc/clawbench-fleet/config.yaml`、创建数据目录、写入并启动 systemd 服务，然后打印管理地址和初始管理员密码。

登录后：

1. 到「升级中心」装一份 Clawbench（服务器能上网就点「从 npm 安装」，内网就上传二进制）。
2. 到「租户」新建一个，或用 CSV 批量导入。
3. 把地址、初始密码和 SSH 命令发给员工（详情页可以直接复制 SSH 命令）。

常用命令：

```bash
systemctl status clawbench-fleet
journalctl -u clawbench-fleet -f
sudo ./deploy/install.sh --uninstall     # 停服务、删二进制，数据保留
```

### 配置

`/etc/clawbench-fleet/config.yaml`，完整示例见 [deploy/config.example.yaml](deploy/config.example.yaml)。常用项：

- `listen` / `data_dir` / `clawbench.install_dir`
- `clawbench.port_start` / `port_end` / `ports_per_tenant`：租户端口段
- `clawbench.uid_start` / `uid_end`：Linux 用户 UID 段
- `backup.hour` / `backup.retention_days`、`trash.retention_days`、`audit.retention_days`
- `clawbench.disk_quota_gb`：新租户默认磁盘上限（GB，0 表示不限制）
- `ssh.enabled` / `ssh.shell` / `ssh.port`：新建租户默认是否开 SSH、登录 shell、对外展示的 SSH 端口（留 0 则问 sshd）
- `tls.cert_dir`：平台管理界面自己的 HTTPS 证书目录（和下面的实例证书无关）
- `tenant_tls.enabled` / `tenant_tls.cert_dir`：给员工实例统一下发的 HTTPS 证书。默认打开，平台自动生成一份自签证书放在 `<data_dir>/certs`，下发给所有实例共用

界面的「设置」页可以直接改端口段、默认配额、备份和保留策略；**端口和 UID 的改动只影响之后新建的租户**。同一页还能：
- 看证书状态、重新生成证书；
- 维护「统一配置下发」的键值表（例如 `chat.page_size` = `50`），保存后点「下发到所有实例」才写进实例并重启它们。端口、监听地址、密码、证书目录这几项由平台自己维护，不能在这里覆盖。

员工看到的访问地址固定用 `https`；地址里的 IP 优先用设置里填的「对外主机」，留空则跟随管理员当前浏览器访问平台所用的那个地址。

---

## 目录结构

```
cmd/fleet/            入口
internal/
  config/             平台配置（yaml）
  domain/             数据模型
  store/              SQLite 数据层（租户/端口/备份/审计/任务/版本/会话）
  sysops/             系统操作层：real.go 走真实命令，fake.go 是开发模式的模拟实现
  fleet/              业务逻辑（租户生命周期、备份恢复、升级回滚、后台循环）
  httpapi/            HTTP 接口
  web/                内嵌前端
web/                  Vue 3 前端源码
deploy/               systemd 单元、配置示例、安装脚本
docs/design.md        设计文档
```

---

## 测试与门禁

本地一条命令跑完 CI 的全部检查（lint、单元测试、集成测试、构建、前后端覆盖率门槛）：

```bash
./scripts/pre-push-checks.sh          # 全量
./scripts/pre-push-checks.sh --skip-coverage   # 跳过覆盖率（快速自查）
```

单项：

```bash
./scripts/lint-go.sh                 # golangci-lint（版本锁定在 .golangci-lint-version）
go test ./...                        # 单元测试（模拟系统，不需要 root）
go test -tags integration ./...      # 集成测试：真实 HTTP + SQLite + 模拟系统
cd web && npm run lint               # ESLint
cd web && npm test                   # 前端单元测试
cd web && npm run build              # 前端类型检查 + 构建
```

门禁规则（与 Clawbench 主干一致）：

| 检查 | 规则 |
|---|---|
| Go lint | `golangci.yaml` 全量严格模式；测试文件同样检查，只放行测试里无法避免的几类（gosec / goconst / noctx / 复杂度） |
| Go 覆盖率 | 两层：① 项目门槛，改动行的包覆盖率不能低于主干基线 1.5 个百分点以上；② 改动行覆盖率 ≥ 80% |
| 前端覆盖率 | 同上两层，统计 `src/api.ts`、`src/utils.ts`、`src/stores/` |
| CI | `.github/workflows/ci.yml`：Lint / Test / Integration / Coverage Gate / Frontend |

主干基线由 CI 在 main 分支构建后上传；本地从没跑过基线时只跑“改动行覆盖”这一层。



装 Git 钩子（提交前只 lint 改动的文件，push 前跑全套）：

```bash
cd web && npm run hooks:install
```

> 本地覆盖率基线放在 `.clawbench-ci/baseline/`，已被 git 忽略，CI 会自动从 main 下载。

---

## 安全边界（重要）

- 磁盘上限是硬限制：平台每 5 分钟量一次家目录，超了会停掉实例、结束这个账号的所有进程（包括 SSH 里跑的命令）、锁定密码，并在状态里写明原因。正在启动的超限实例会被拦住，不让它一启动就继续写满磁盘。磁盘是后来才统计的，所以一个瞬间写入巨量数据仍可能先涨上去再被停下来。
- 每个租户是一个**不能 sudo** 的 Linux 服务账号，家目录权限 700。新租户默认开 SSH：账号有 `bash` 登录 shell 和一个固定密码，这个密码和 Clawbench 网页登录是同一个，只在创建、重置或重新开启 SSH 时显示一次。关掉 SSH 会把 shell 改回 `nologin` 并锁定密码。通过「扫描已有实例」纳管进来的老账号默认不开 SSH，平台保留它原来的 shell、密码和登录方式，只接管服务。
- 平台**不会修改服务器的 sshd 配置**，只检查 sshd 是否安装、是否运行、是否允许密码登录；没配好会在设置页和租户详情页提示。服务器要求只允许密钥登录时，SSH 功能就按服务器策略走。
- 实例以 `User=<租户>` 跑，`--managed` 禁止员工自己升级或重启实例。
- 管理界面默认 HTTP 且只应暴露在管理网段；要放公网请配 TLS 或用反向代理。
- 这套隔离防的是**误操作和资源抢占**，不是防恶意租户：员工能在自己的实例里运行任意命令、能读到自己的数据。客户要求租户之间绝对隔离的话，需要容器或虚拟机。

更多细节见 [docs/design.md](docs/design.md) 的“安全设计”和“失败处理清单”。
