# Clawbench Fleet

面向企业 IT 的 **Clawbench 实例管理平台**：在一台 Linux 服务器上批量创建 Linux 用户，为每个用户运行一个独立的 Clawbench 实例，并用一套 Web 界面统一管理这些实例。

设计文档：[docs/design.md](docs/design.md)

---

## 能做什么

| 能力 | 说明 |
|---|---|
| 批量建号 | 单个创建，或用 CSV 一次导入几十到几百个；导入前先预览重名、格式和端口占用 |
| 实例运维 | 启动、停止、重启、重置密码、修复、批量操作 |
| 状态与用量 | 实例存活、Clawbench 版本、CPU / 内存 / 进程数 / 磁盘占用，机器整体负载 |
| 日志 | 直接看 systemd journal 最近若干行，不用登服务器 |
| 备份与恢复 | 每天自动备份数据库和配置，可手动备份、一键恢复、下载 |
| 回收站 | 删除后整个家目录保留 30 天，可恢复账号、端口和数据；也可以立即强制删除，跳过保留期 |
| 版本升级 | 上传 Clawbench 二进制 → 试点一个实例 → 批量升级 → 一键回滚，失败自动回退 |
| 审计 | 所有写操作留痕，保留 180 天，可按操作人、动作、租户、时间筛选 |
| 资源限制 | 每个实例用 systemd 限制 CPU、内存、进程数，磁盘只统计告警 |

明确不做：员工门户、网页终端、浏览员工文件、多机调度、模型密钥统一下发。

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

员工浏览器 ──HTTP──> IP:21000  clawbench (User=alice, systemd)
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

1. 到「升级中心」上传一份 Clawbench 二进制（平台靠它来部署实例）。
2. 到「租户」新建一个，或用 CSV 批量导入。
3. 把地址和初始密码发给员工。

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
- `tls.cert_dir`：填了就启用 HTTPS

界面的「设置」页可以直接改端口段、默认配额、备份和保留策略；**端口和 UID 的改动只影响之后新建的租户**。

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

## 测试

```bash
go test ./...        # 后端单元与集成测试（用模拟系统，不需要 root）
cd web && npm test   # 前端单元测试
cd web && npm run build   # 前端类型检查 + 构建
```

---

## 安全边界（重要）

- 每个租户是一个**不能 SSH 登录、没有密码、不能 sudo** 的 Linux 服务账号，家目录权限 700。
- 实例以 `User=<租户>` 跑，`--managed` 禁止员工自己升级或重启实例。
- 管理界面默认 HTTP 且只应暴露在管理网段；要放公网请配 TLS 或用反向代理。
- 这套隔离防的是**误操作和资源抢占**，不是防恶意租户：员工能在自己的实例里运行任意命令、能读到自己的数据。客户要求租户之间绝对隔离的话，需要容器或虚拟机。

更多细节见 [docs/design.md](docs/design.md) 的“安全设计”和“失败处理清单”。
