# Clawbench Fleet 设计文档

- 状态：**第一版已实现**（2026-11-20），入口 `cmd/fleet`，本地体验见 README 的“快速开始”
- 日期：2026-11-19
- 项目路径：`/home/xulongzhe/projects/clawbench-fleet`
- 技术栈：Go 后端 + Vue 3 前端，单二进制内嵌前端

---

## 1. 一句话

给企业 IT 用的 Clawbench 实例管理平台：在一台 Linux 服务器上批量创建 Linux 用户，为每个用户跑一个独立的 Clawbench 实例，并通过一个 Web 界面统一管理这些实例。

---

## 2. 目标与非目标

### 2.1 目标

- 批量建号：一次导入几十到几百个员工，自动建 Linux 账号、分配端口、生成配置、拉起服务。
- 统一运维：启停、重启、看日志、重置密码、看资源用量、备份恢复、升级回滚。
- 出错可追：所有写操作留审计记录，谁在什么时候对谁做了什么。
- 出错可退：升级失败能回滚，删除有回收站，备份能恢复。
- 部署简单：一个二进制 + 一个 systemd 服务，不依赖外部数据库和消息队列。

### 2.2 非目标（第一版明确不做）

- 不做员工门户。员工只拿到地址和密码，直接访问自己的实例。
- 不做网页终端和文件浏览/下载。避免管理员通过浏览器直接拿到服务器 shell 或员工数据。
- 不做多机调度。第一版只管一台服务器。
- 不做模型密钥统一下发。员工在自己的实例里配置模型。
- 不做在线人数统计。看板只做平台能直接拿到的系统指标。
- 不做容器/虚拟机级隔离。租户之间是 Linux 用户 + systemd 资源限制级别的隔离。

### 2.3 以后可能做

- 多管理员和角色权限（超管、只读运维、审计）。
- 对接企业通讯录/LDAP，离职自动停用。
- 员工门户 + 免密跳转。
  - 2026-11-20 已补一个更小的版本：`/guest` 免登录只读页，只显示显示名、登录名、端口、地址、状态，可报障；不能进入实例。
- 多机调度（每台机器一个 agent）。
- 钉钉/飞书/邮件告警。
- 接管已经在跑的 Clawbench 实例。

---

## 3. 角色与使用方式

### 3.1 管理员

- 打开 `http://<服务器IP>:30000`，用平台自己的账号登录。
- 在界面上建号、导入、启停、重置密码、看日志、备份、升级。
- 平台只有一个管理员账号（第一版），首次启动生成随机密码。

### 3.2 员工

- 拿到管理员给的地址（例如 `http://10.0.0.5:21001`）和初始密码。
- 直接在浏览器打开自己的 Clawbench，登录后使用。
- 员工可以在自己的实例里改密码、配模型、装 Skill、跑终端。
- 开了 SSH 的租户可以用同一个密码 `ssh 用户名@服务器` 登录；密码只在创建、重置或重新开启 SSH 时显示一次。员工仍然**不能** sudo。
- 平台不修改 sshd 配置：服务器要求密钥登录或关闭密码登录时，以服务器策略为准。

### 3.3 安全边界（必须说清楚）

资源上限的执行方式：

- CPU、内存、进程数交给 systemd 的 cgroup 限制，超了会被系统直接卡住或杀掉。
- 磁盘上限每 5 分钟量一次家目录。超限时平台停掉实例、按进程属主结束这个账号的所有进程（包括从 SSH 进来的 shell，不只是实例本体）、锁定 Linux 密码，并把原因写进实例状态；启动或重启前再量一次，仍然超限就不让起来。管理员清理文件或调大上限后点「启动」即可恢复。
- 用量历史每 5 分钟记一个点，默认保留 7 天，可在设置页改成 1-90 天。

这套隔离防的是“误操作”和“资源抢占”，**不是**防恶意租户。

- 员工在自己的实例里有终端，可以以自己的 Linux 用户身份运行任意命令；开了 SSH 的账号还能从外部登录，权限边界仍然是这个 Linux 用户。
- 员工能读到自己家目录下的所有文件，包括自己的 `config.yaml` 和数据库。
- 员工之间不能读对方的家目录（Linux 权限 700），但不能防内核漏洞或提权漏洞。
- 如果客户要求“员工之间绝对不能互相攻击”，需要上容器或虚拟机，这不是本项目的范围。

---

## 4. 总体架构

```
                          管理员浏览器
                               │ HTTP
                               ▼
                 ┌──────────────────────────┐
                 │  clawbench-fleet (root)  │  :30000
                 │  Go 后端 + 内嵌 Vue 前端  │
                 └──────┬───────────────────┘
                        │
        ┌───────────────┼────────────────┬──────────────────┐
        ▼               ▼                ▼                  ▼
   SQLite          systemd/useradd   /opt/clawbench-fleet   /var/lib/clawbench-fleet
   fleet.db         journalctl        versions/<版本>/       backups/ trash/ logs/
                                         │
                                         │ current 软链
                                         ▼
                              /opt/clawbench-fleet/current/clawbench
                                         │
              ┌──────────────────────────┼──────────────────────────┐
              ▼                          ▼                          ▼
   clawbench-alice.service     clawbench-bob.service      clawbench-carol.service
   User=alice :21001           User=bob :21004            User=carol :21007
   /home/alice/.clawbench      /home/bob/.clawbench       /home/carol/.clawbench
              ▲                          ▲                          ▲
              │                          │                          │
        员工浏览器                  员工浏览器                  员工浏览器
```

### 4.1 组件

| 组件 | 职责 |
|---|---|
| `clawbench-fleet` | 平台后端。以 root 运行，负责建号、写 systemd 单元、启停服务、备份、升级、审计 |
| Fleet Web | 平台前端。Vue 3 + Vite，构建后内嵌进 Go 二进制 |
| `fleet.db` | 平台自己的 SQLite。存租户、端口、备份、审计、管理员、版本、任务 |
| systemd | 真正托管每个租户的 Clawbench 进程，负责开机自启、崩溃重启、资源限制 |
| `clawbench` | 被管理的实例程序。全机一份，通过 `current` 软链切换版本 |

### 4.2 为什么平台要独立跑、而且要 root

- 建 Linux 用户、改家目录属主、写 systemd 单元、启停服务，全都需要 root。
- 把这些能力放进 Clawbench 本体，等于把 root 能力塞进每个员工天天用的 Web 应用，攻击面太大。
- 独立进程可以把权限收在一个地方：只有 `clawbench-fleet` 以 root 跑，Clawbench 实例都以普通租户用户跑。

---

## 5. 目录与端口规划

### 5.1 目录

| 路径 | 用途 | 属主/权限 |
|---|---|---|
| `/opt/clawbench-fleet/versions/<版本>/clawbench` | 每个版本的 Clawbench 二进制 | root:root 0755 |
| `/opt/clawbench-fleet/current` | 指向当前版本的软链 | root:root |
| `/etc/clawbench-fleet/config.yaml` | 平台自己的配置 | root:root 0600 |
| `/var/lib/clawbench-fleet/fleet.db` | 平台数据库 | root:root 0600 |
| `/var/lib/clawbench-fleet/backups/<登录名>/<时间>/` | 租户备份 | root:root 0600 |
| `/var/lib/clawbench-fleet/trash/<登录名>-<时间>/` | 回收站 | root:root 0700 |
| `/var/lib/clawbench-fleet/uploads/` | 升级包临时上传目录 | root:root 0700 |
| `/var/lib/clawbench-fleet/logs/` | 平台自己的日志 | root:root 0700 |
| `/home/<登录名>` | 租户家目录，也是工作目录 | 租户:租户 0700 |
| `/home/<登录名>/.clawbench` | 租户的 Clawbench 数据目录 | 租户:租户 0700 |
| `/home/<登录名>/.clawbench/config/config.yaml` | 租户实例配置 | 租户:租户 0600 |
| `/home/<登录名>/.clawbench/ClawBench.db` | 租户实例数据库 | 租户:租户 0600 |

### 5.2 端口

| 用途 | 默认值 |
|---|---|
| 平台管理界面 | 30000 |
| 租户实例端口范围 | 21000–29999 |
| 每个租户预留端口数 | 3 个（主端口 + 2 个预留） |

为什么每个租户要占 3 个端口：

- 主端口：Clawbench 的 Web 和 API。
- 主端口 +1：如果租户把端口转发从默认的 `h2` 改成 `ssh`，Clawbench 会在这个端口开 SSH 隧道。
- 主端口 +2：如果租户开了 TLS，Clawbench 的开发代理默认用这个端口。

第一版默认把租户的 `port_forward.transport` 固定为 `h2`、TLS 关闭，所以实际只占主端口；但分配时仍按 3 个一组留空，避免以后有人打开这两个功能就撞端口。

### 5.3 端口分配规则

- 从范围起点开始，找第一组连续 3 个空闲端口。
- 删除租户时，端口跟着进回收站，**不立即释放**；回收站清空后才回到空闲池。这样恢复租户时端口不变，员工收藏的地址不会失效。
- 端口范围、每组端口数写在平台配置里，改了只影响新租户。

---

## 6. 租户模型与状态机

### 6.1 租户字段

| 字段 | 说明 |
|---|---|
| `id` | 平台内部自增 ID |
| `username` | Linux 登录名，也是唯一标识；建好后不允许改 |
| `display_name` | 显示名，可改 |
| `uid` / `gid` | Linux 用户 ID / 组 ID |
| `port` | 主端口 |
| `status` | `creating` / `running` / `stopped` / `error` / `deleting` / `trashed` |
| `version` | 最近一次健康检查报告的 Clawbench 版本 |
| `cpu_quota` | systemd `CPUQuota`，例如 `200%` |
| `memory_max` | systemd `MemoryMax`，例如 `4G` |
| `tasks_max` | systemd `TasksMax`，例如 `512` |
| `home_dir` / `data_dir` | 家目录、数据目录 |
| `unit_name` | systemd 单元名，`clawbench-<username>.service` |
| `notes` | 管理员备注 |
| `disk_bytes` / `disk_checked_at` | 家目录占用，每天算一次 |
| `last_health_at` / `last_health_ok` | 最近一次健康检查 |
| `last_error` | 最近一次失败原因 |
| `created_at` / `updated_at` | 时间 |
| `deleted_at` / `trash_path` | 删除时间、回收站路径 |

> `root` 是唯一受保护的账号：可以启停，但不能删除。这个属性不存字段，按登录名判断。

### 6.2 登录名规则

- 3–32 个字符。
- 只能用小写字母、数字、下划线、短横线。
- 必须以小写字母开头。
- 不能是系统保留名：`root`、`daemon`、`bin`、`sys`、`sync`、`clawbench`、`clawbench-fleet`、`nobody` 等。
- 同一台机器上不能重复。

### 6.3 状态机

```
                 ┌──────────────┐
                 │   creating   │
                 └──────┬───────┘
                  成功  │  失败
              ┌─────────┴─────────┐
              ▼                   ▼
        ┌──────────┐        ┌──────────┐
        │ running  │◄──────►│  error   │
        └────┬─────┘  修复   └──────────┘
             │
       停止  │  启动
             ▼
        ┌──────────┐
        │ stopped  │
        └────┬─────┘
             │ 删除
             ▼
        ┌──────────┐
        │ deleting │
        └────┬─────┘
             ▼
        ┌──────────┐   30 天   ┌──────────┐
        │ trashed  │─────────►│ 彻底删除  │
        └────┬─────┘           └──────────┘
             │ 恢复
             ▼
        ┌──────────┐
        │ running  │
        └──────────┘
```

说明：

- “停止”= `systemctl disable --now`，服务器重启也不会自己起来。
- “启动”= `systemctl enable --now`。
- “停用”和“停止”是同一个动作，只是批量操作时叫停用。
- `error` 表示服务起不来或健康检查一直失败，详情页能看到日志和最近错误。

---

## 7. 关键流程

### 7.1 建号（单个）

输入：登录名、显示名、备注、可选配额（不填用默认值）、是否开 SSH（默认按平台设置）。

步骤：

1. 校验登录名规则、是否与现有租户或系统用户重名。
2. 在端口池里找一组连续 3 个空闲端口。
3. 创建 Linux 用户：
   - `useradd --create-home --shell /usr/sbin/nologin --user-group --uid <uid> <username>`
   - UID 从配置的范围里找空闲值（默认 3000–9999）。
   - 家目录权限 0700。
4. 生成初始密码（12 位，字母 + 数字，避开容易混淆的字符）。
   - 开了 SSH：`usermod -s /bin/bash`，再用 `chpasswd` 通过标准输入设置同一个密码，密码不出现在命令行里。
   - 没开 SSH：保留 `nologin`，并用 `passwd -l` 锁定密码。
5. 写租户配置 `~/.clawbench/config/config.yaml`：
   ```yaml
   port: <主端口>
   host: 0.0.0.0
   password: "sha256:<哈希>"
   port_forward:
     enabled: false
     transport: h2
   ```
   文件属主改为租户，权限 0600。
6. 生成 systemd 单元 `/etc/systemd/system/clawbench-<username>.service`（见第 8 节）。
7. `systemctl daemon-reload`、`systemctl enable --now clawbench-<username>.service`。
8. 健康检查：最多等 60 秒，轮询 `http://127.0.0.1:<端口>/api/health`，要求返回 `app=clawbench`。
9. 成功：状态 `running`，版本从健康检查里取；把初始密码一次性返回给前端。
10. 失败：按相反顺序回滚（停服务、删单元、删用户、删家目录、释放端口），状态记为 `error`，返回具体原因。

注意：

- 初始密码只在创建成功的响应里出现一次，平台不存明文。
- 如果管理员没保存，只能重置密码。

### 7.2 CSV 批量导入

CSV 格式（UTF-8，带表头）：

```csv
username,display_name,notes
alice,张三,研发部
bob,李四,产品部
```

预览（不写任何数据）：

- 逐行校验登录名、重名、端口是否够。
- 页面显示：可导入 N 条、有问题 M 条；问题行标红并说明原因。
- 端口不够时直接拒绝，不开始导入。

导入：

- 创建一个导入任务，后台逐行执行 7.1 的步骤。
- 单行失败不影响后面的行，失败原因记在任务明细里。
- 页面显示进度（已完成/总数/失败数），可离开页面，任务继续跑。
- 结束后提供两份 CSV 下载：
  - 成功清单：登录名、地址、初始密码（**只生成一次，不落库**）。
  - 失败清单：登录名、失败原因。

安全提醒：

- 成功清单里有明文密码，下载后请尽快发给员工并删除文件。
- 平台不保存明文密码，丢了只能重置。

### 7.3 启停 / 重启 / 批量操作

| 操作 | 实际动作 | 状态 |
|---|---|---|
| 启动 | `systemctl enable --now` | `running` |
| 停止 | `systemctl disable --now` | `stopped` |
| 重启 | `systemctl restart` | `running`（失败则 `error`） |
| 批量启动/停止/重启 | 对选中的租户逐个执行，最多 4 个并发 | 任务进度 |

批量操作规则：

- 每个租户同一时间只允许一个操作，重复请求返回 409。
- 批量任务失败不整体回滚（启停本身是可重试的），但会记录每个失败项。
- 任务结束后给一份结果清单。

### 7.4 密码重置

1. 生成 12 位新密码（字母 + 数字）。
2. 计算 `sha256(密码 + "clawbench-salt")`，写成 `password: "sha256:<hex>"`。
3. 写配置文件：先写临时文件，改属主和权限，再原子替换，避免写一半。
4. 重启该实例。
5. 健康检查通过后，把新密码一次性返回给前端。
6. 记审计（不记录密码本身）。

说明：

- Clawbench 只存哈希，平台看不到旧密码，所以只能覆盖。
- 员工自己也可以在实例里改密码；改完后平台不知道新密码，但管理员仍然可以强制重置。
- 重置会让该员工当前的登录会话失效（Clawbench 改密会轮换会话令牌）。

### 7.5 查看日志

- 主来源：`journalctl -u clawbench-<username>.service -n <行数> --no-pager -o short-iso`。
- 可选补充：`~/.clawbench/logs/` 下的日志文件，只读最近若干行。
- 页面只提供“看”，不提供下载整个日志文件。
- 默认 500 行，最多 5000 行。
- 日志内容可能包含员工的项目路径和对话片段，属于敏感信息，查看动作记审计。

### 7.6 用量看板

每个租户显示：

- 状态（运行中/已停止/异常）。
- CPU 使用率：读 systemd cgroup 的 CPU 累计值，按两次采样算百分比。
- 内存：读 cgroup 当前内存。
- 进程数：读 cgroup 当前 PID 数。
- 磁盘：每天凌晨 `du -sb /home/<username>` 一次，结果缓存到数据库；页面显示缓存值和统计时间，不实时算。
- 磁盘提醒：家目录超过设置里的阈值（默认 5GB）时，在总览页单独列出来；只提醒，不自动停服务。
- 版本、端口、地址、最后健康检查时间。

总览页显示：

- 机器总 CPU、内存、磁盘。
- 各状态租户数量。
- 异常租户列表（起不来、健康检查失败、磁盘超阈值）。
- 最近操作记录。

明确不做：在线人数、会话数。原因：平台拿不到实例内部数据，除非改 Clawbench 或集中保管密码。

### 7.7 备份

- 频率：每天凌晨 3 点（可配置）。
- 范围：只备 Clawbench 数据目录里的**数据库和配置**，不备项目代码、附件、生成的图片语音。
- 方式：
  - 实例在跑：用 SQLite 的 `VACUUM INTO` 生成一致性快照，不停实例。
  - 实例已停：直接复制数据库文件。
  - 配置目录直接复制。
- 压缩：gzip。
- 存放：`/var/lib/clawbench-fleet/backups/<username>/<YYYYMMDD-HHMMSS>/`，权限 0600，只有 root 能读。
- 保留：默认 7 天（可配置），超期自动删除。
- 校验：备份完成后打开快照执行 `PRAGMA integrity_check`，失败标记为坏备份并保留现场。
- 磁盘不足：暂停当天的备份，在总览页显示警告，不删除旧备份来“腾地方”（避免把最后的退路删掉）。

备份内容含配置里的模型密钥和密码哈希，属于敏感文件；下载备份也要记审计。

### 7.8 恢复

默认语义：**把数据库恢复到某个备份点，配置保持当前不变。**

步骤：

1. 停止实例。
2. 校验备份文件完整性。
3. 备份当前数据库（以防恢复错了还能回去）。
4. 用备份的数据库替换 `ClawBench.db`，同时删除 `-wal` / `-shm`。
5. 启动实例，健康检查。
6. 记审计。

为什么默认不动配置：

- 备份里的配置可能带着旧密码、旧模型密钥，恢复配置会把员工后来改的东西一起退回去，容易造成“密码突然变了”。
- 备份包里仍然包含配置，需要时可以手动取用。

### 7.9 删除 / 回收站 / 恢复租户

删除步骤：

1. 二次确认：要求输入登录名。
2. 停止并禁用服务。
3. 删除 systemd 单元。
4. 统计家目录大小，显示给管理员确认。
5. 把整个 `/home/<username>` 移到 `/var/lib/clawbench-fleet/trash/<username>-<时间>/home`。
6. 写一份 `meta.json`：登录名、UID/GID、端口、显示名、删除时间、原家目录。
7. `userdel <username>`（家目录已经移走，不加 `-r`）。
8. 端口标记为“回收站占用”，不释放。
9. 状态改为 `trashed`。

回收站页面：

- 列出已删除租户、删除时间、占用大小、剩余天数。
- 操作：恢复、立即强制删除（跳过剩余保留期）。
- 30 天后自动彻底删除（可配置）。

强制删除的入口：

- 租户详情页点“删除”后，弹窗会问“稍后处理”还是“立即强制删除”。
- 已删除租户的详情页顶部直接提供“恢复租户”和“强制删除”。
- 回收站列表里每个租户都有“强制删除”按钮。
- 未删除的租户不能直接强制删除，后端会拒绝（必须先走删除进回收站）。

恢复租户：

1. 检查登录名是否又被占用。
2. 重新建 Linux 用户（优先复用原 UID；被占用则用新 UID）。
3. 把家目录移回去，`chown -R` 成新 UID/GID。
4. 重写 systemd 单元，端口用原来的。
5. 启动并健康检查。
6. 状态回到 `running`。

彻底删除（强制删除）：

- 只允许对 `trashed` 状态的租户执行；未删除的租户直接拒绝。
- 删除回收站目录。
- 释放端口。
- 删除租户记录（或保留一条已删除标记，视审计需要）。

### 7.10 升级 / 回滚

版本存放：

```
/opt/clawbench-fleet/versions/1.2.3/clawbench
/opt/clawbench-fleet/versions/1.2.4/clawbench
/opt/clawbench-fleet/current -> versions/1.2.4
```

获取新版本（两条路，结果完全一样）：

**A. 从 npm 安装（主路径）**

1. 升级中心向 npm 查询 `@xulongzhe/clawbench` 的已发布版本，列表里标出本机已有的，并显示当前实际用的是哪个源（配置文件指定 / 服务器默认）。
2. 管理员选一个版本点“安装到本机”。
3. 平台执行 `npm install --prefix <临时目录> @xulongzhe/clawbench@<版本>`。装到临时目录而不是全局，是为了多个版本能共存。
4. 从 `node_modules/@xulongzhe/clawbench-linux-<arch>/bin/clawbench` 取出可执行文件，复制到 `versions/<版本>/clawbench`，权限 0755。
5. 校验：能执行 `--version` 并输出版本号；记录 SHA-256；删掉临时目录。
6. 服务器没装 npm 或连不上 npm 时，页面提示原因，改用下面的上传方式。

**B. 离线安装（上传文件）**

1. 管理员在升级中心上传 Clawbench 二进制。
2. 平台校验：文件是 Linux 可执行文件、大小合理、能执行 `--version` 并输出版本号。
3. 保存到 `versions/<版本>/clawbench`，权限 0755，记录 SHA-256。

两条路的版本号都以程序自己 `--version` 的输出为准，所以目录名、页面显示、健康检查三处永远一致（`v0.143.0` 和 `0.143.0` 视为同一版）。

试点：

1. 管理员选一个试点租户（默认选第一个运行中的）。
2. 把 `current` 切到新版本。
3. 重启试点租户，最多等 60 秒健康检查，并要求 `/api/health` 返回的版本等于新版本。
4. 失败：把 `current` 切回旧版本，重启试点，标记升级失败。

批量：

1. 试点通过后，管理员确认批量。
2. 逐个重启租户（最多 4 个并发），每个都做健康检查和版本校验。
3. 任一失败：立即停止后续，把 `current` 切回旧版本，把所有已经升级的租户重启回旧版本。
4. 全部成功：记录每个租户的新版本。

回滚：

- 管理员可以在升级中心点“回滚到上一版”。
- 把 `current` 切到上一版，批量重启所有租户。
- 回滚同样逐個健康检查，失败则停止并报告。

注意：

- 正在运行的进程不会因为软链变化而自动换版本；只有重启才会加载新版本。
- 升级期间员工会短暂断线（重启实例），页面要提前提示。
- Clawbench 有客户端/服务端版本兼容检查，升级服务端后旧客户端可能被拦；升级前要提示管理员。

平台自身升级（升级平台，不是升级实例）：

- 升级中心多一个「升级平台自身」区块，上传的是 `clawbench-fleet` 自己的二进制，和第 7.10 节升级租户实例完全是两条线，互不影响。
- 顺序固定为：写临时文件 → 跑 `--version` 读出上传的版本 → 备份当前程序到 `<程序路径>.bak` → 原子替换 → 重启 `clawbench-fleet.service`。
- 所有能失败的事都在替换之前：文件不是可运行程序、版本和当前完全一样、程序目录不可写、备份失败，都会直接报错，此时磁盘上的程序一个字节都没变，平台照常运行，可以重试。
- 替换用的是「先写到同目录临时文件再改名」，任何一步失败都保证目录里仍然有一个能启动的程序，不会出现「装到一半没有二进制」的不可恢复状态。
- 重启之后这个进程就没了，HTTP 响应必须在重启之前写出去。页面收到「已开始重启」后改成每 2 秒问一次 `/api/health`，版本对上才刷新，最多等 3 分钟。
- 重启命令发出失败（例如 systemd 没在跑）时，新程序已经在磁盘上但进程还是旧的，接口会同时返回结果和一句警告，提示手工执行 `systemctl restart clawbench-fleet`。
- `--dev` 模式下不允许：开发时跑的就是本地构建出来的程序，替换没有意义，还会把文件换掉却不重启。
- 回退办法：停服务，把 `<程序路径>.bak` 覆盖回去，再启动。

### 7.11 健康检查与对账

- 每 30 秒检查一次所有 `running` 状态的租户：
  - `systemctl is-active` 是否为 `active`。
  - `http://127.0.0.1:<端口>/api/health` 是否返回 `app=clawbench`。
  - 实例自己开了 HTTPS 时，HTTP 请求会收到 301，平台跟着跳转到 HTTPS 再问一次。证书是实例自己签的，平台只对回环地址放宽证书校验，返回内容仍要求 `app=clawbench`。
- 连续 2 次失败标记为 `error`，记录原因，不自动删除任何东西。
- systemd 自身配置 `Restart=always`，进程崩溃会自动拉起；健康检查只负责“发现并展示”，不替代 systemd。
- 平台启动时做一次对账：
  - 扫描 `/etc/systemd/system/clawbench-*.service`，和数据库比对。
  - 数据库有、单元没有：标记 `error`，提示“单元丢失，可修复”。
  - 单元有、数据库没有：标记为“孤儿”，只展示，不自动删除。
  - `creating` 状态超过 10 分钟：标记 `error`，提供“重试/删除”。

### 7.12 扫描与纳管已有实例

平台第一次接管一台已经有人在用的机器时，需要把之前手工建的用户和实例收编进来。

**怎么判定一个账号是租户**

1. `/etc/passwd` 里有这个用户，家目录不是 `/`。
2. 家目录下有 `.clawbench` 数据目录（如果已有 systemd 单元里写了 `--data-dir`，以那个为准；家目录下没有但进程用 `--data-dir` 指向别处，也算）。
3. 端口按这个顺序取：**进程实际在监听的端口** → 已有 systemd 单元里的 `--port` → `config.yaml` 里的 `port`。三个都没有就列出来但标成不可纳管。

为什么要先看进程：`--port` 是启动参数，Clawbench 不会把它写回 `config.yaml`，配置文件里可能一直是老值（比如 21000）。只读配置的话，扫描出来的端口就会全是一个数。另外，一个实例还会为端口转发多开几个监听端口，所以进程在听的端口里，优先取和配置文件/单元一致的那个，避免把转发端口当成主端口。扫描结果里会写明端口来源：`运行中的进程` / `systemd 单元` / `配置文件`。

**扫描（手动触发，只读）**

- 平台遍历本机用户，符合条件的列出来：登录名、UID、家目录、端口及来源、是否已有 systemd 单元、服务是否在跑、健康检查是否通过、版本、现有配额。
- 已经在管理列表里的账号也会列出来，标“已纳管”，不可勾选。
- 端口已经被别的租户占用的，标红不可勾选。
- 主端口有进程在听、但 HTTP 和 HTTPS 健康检查都不通的，标红不可纳管，并写明原因。实例自己开了 HTTPS 不影响纳管：平台会跟着它的跳转用 HTTPS 再问一次。
- 平台还没装 Clawbench 版本时全部标红，先去升级中心装一份。
- 扫描不写数据库、不碰服务。

**纳管（勾选后执行）**

1. 写一条租户记录，端口登记到端口表（不会被新租户抢走），状态 `creating`。
2. 改写 `<数据目录>/config/config.yaml`：保留原有配置和密码，只统一端口、监听地址和端口转发开关。**不重置员工密码。**
3. 按第 8 节的模板写 `clawbench-<用户名>.service`，`daemon-reload`、`enable`，然后重启一次。
4. 如果原来没有 systemd 单元、实例是手工起的：先确认占着端口的进程确实属于这个用户，再停掉它，然后交给 systemd；进程属于别人就报错，绝不动别人的进程。
5. 健康检查通过 → `running`；失败 → `error` 并保留记录，可以用“修复”或“删除”处理。

纳管会短暂中断实例几秒。已经纳管的账号再次扫描会显示“已纳管”。

一次勾选多个账号时，平台最多并行处理 3 个：每个账号是一个后台任务项，页面能实时看到“排队中 / 处理中 / 已纳入 / 失败”和失败原因，不用干等一个长请求。整批只读取一次系统用户、单元和监听端口，不再为每个账号重复扫全机。

**改端口**

已经纳管的实例可以在详情页改主端口。平台会先确认新端口没有被其他租户或本机程序占用，然后在端口表里一次性把旧端口释放、新端口登记到这个租户，再重写 `config.yaml` 和 systemd 单元；运行中的实例会重启一次并按新端口做健康检查。员工访问地址随之变化，需要重新通知员工。

**root 的特殊处理**

- root 有 `.clawbench` 数据目录时同样纳管，家目录按 `/etc/passwd` 的真实值（通常是 `/root`），不是 `/home/root`。
- root 可以启动、停止、重启、看日志、备份，但**不能删除**：删除接口直接拒绝，界面上删除按钮置灰。这一点在数据模型里不额外存字段，按登录名 `root` 判定。

### 7.13 默认资源上限下发

设置页的默认资源上限（CPU / 内存 / 进程数）可以随时修改，保存后只影响新建租户。要让已有实例也用上，点“应用到所有实例”：

1. 平台先保存当前设置，再列出所有需要改的实例。回收站、正在创建、正在删除的实例跳过；已经是目标值的实例也不重复处理。
2. 每个实例作为一个后台任务项，最多并行处理 3 个。运行中的实例会重写 systemd 单元、重启一次并等待健康检查；已停止或启动失败的实例只改单元，下次启动生效。
3. 已经有单独设置过配额的实例也会被覆盖，确认弹窗会明确提示这一点。
4. 任务进度和每个实例的结果都在弹窗里实时显示，关掉弹窗任务仍在后台继续。

---

## 8. systemd 单元模板

每个租户一个单元文件：`/etc/systemd/system/clawbench-<username>.service`

```ini
[Unit]
Description=Clawbench instance for <username>
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=<username>
Group=<username>
WorkingDirectory=/home/<username>
Environment=HOME=/home/<username>
Environment=PATH=/usr/local/bin:/usr/bin:/bin
ExecStart=/opt/clawbench-fleet/current/clawbench --port <port> --data-dir /home/<username>/.clawbench --managed
Restart=always
RestartSec=3
TimeoutStopSec=30
LimitNOFILE=65536
CPUQuota=<cpu_quota>
MemoryMax=<memory_max>
TasksMax=<tasks_max>
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

说明：

- `--managed` 关闭实例自己的升级和重启接口，防止员工绕过平台换二进制。
- `--port` 覆盖配置文件里的端口，员工改配置也改不了对外端口。
- 不设置 `PrivateTmp`、`ProtectHome` 等会影响终端和工具运行的选项。
- 资源限制用 systemd 的 cgroup 能力；需要 cgroup v2（Ubuntu 22.04+/Debian 12+ 默认满足）。

---

## 9. 页面设计

| 页面 | 作用 | 关键内容 |
|---|---|---|
| 登录 | 管理员登录 | 账号、密码、失败提示 |
| 总览 | 一眼看全局 | 机器 CPU/内存/磁盘、状态计数、异常列表、最近操作 |
| 租户列表 | 日常主页面 | 搜索、状态筛选、分页、批量选择、批量启停/重启、扫描并纳管已有实例 |
| 租户详情 | 单个租户的全部操作 | 地址/端口/版本/状态、启动/停止/重启、重置密码、配额修改、日志、备份列表、操作记录 |
| 新建租户 | 单个建号 | 登录名、显示名、备注、配额；成功后弹窗显示地址和初始密码 |
| 批量导入 | CSV 导入 | 上传、预览（可导入/有问题）、开始导入、进度、下载成功/失败清单 |
| 升级中心 | 版本管理 | 当前版本、从 npm 安装、离线上传、已安装版本、试点、批量升级、回滚、任务进度；另有「升级平台自身」区块 |
| 回收站 | 误删恢复 | 已删除租户、占用大小、剩余天数、恢复、强制删除 |
| 审计日志 | 追责 | 时间、操作人、动作、对象、结果、IP；按条件筛选 |
| 设置 | 平台配置 | 端口范围、默认配额（可一键下发到所有实例）、备份时间/保留天数、回收站天数、审计保留天数、改管理员密码 |

界面风格：

- 沿用 Clawbench 的配色、间距和圆角，不引入重型 UI 框架。
- 第一版只做中文，文案集中管理，方便以后加英文。
- 所有危险操作（删除、回滚、恢复）都要二次确认，并要求输入登录名或版本号。

---

## 10. 接口设计

统一前缀 `/api`，返回 JSON。错误格式：

```json
{
  "error": {
    "code": "port_exhausted",
    "message": "端口范围里没有连续 3 个空闲端口了",
    "details": {}
  }
}
```

### 10.1 认证

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/auth/login` | 登录，设置会话 Cookie |
| POST | `/api/auth/logout` | 退出 |
| GET | `/api/auth/me` | 当前管理员信息 |
| POST | `/api/auth/password` | 修改管理员密码 |

### 10.2 总览

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/overview` | 机器指标、状态计数、异常列表、最近操作 |

### 10.3 租户

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/tenants` | 列表，支持 `q`、`status`、`page`、`page_size` |
| POST | `/api/tenants` | 新建，返回租户信息和一次性初始密码 |
| GET | `/api/tenants/{id}` | 详情 |
| PATCH | `/api/tenants/{id}` | 改显示名、备注、配额、端口；只有配额或端口变化才会重写单元并重启 |
| DELETE | `/api/tenants/{id}` | 删除到回收站，要求 body 里带登录名确认 |
| POST | `/api/tenants/{id}/start` | 启动 |
| POST | `/api/tenants/{id}/stop` | 停止 |
| POST | `/api/tenants/{id}/restart` | 重启 |
| POST | `/api/tenants/{id}/reset-password` | 重置密码，返回一次性新密码 |
| GET | `/api/tenants/{id}/logs` | 日志，`lines` 参数 |
| GET | `/api/tenants/{id}/metrics` | CPU/内存/进程数/磁盘缓存 |
| POST | `/api/tenants/{id}/repair` | 重写配置和 systemd 单元并重启 |
| POST | `/api/tenants/bulk` | 批量操作，body：`{action, ids}` |
| GET | `/api/tenants/scan` | 扫描本机已有实例，返回可纳管的账号列表，不落库 |
| POST | `/api/tenants/adopt` | 纳管已有实例，body：`{usernames: []}`，返回后台任务，按任务项查看逐个结果 |
| POST | `/api/tenants/import/preview` | 上传 CSV 预览，不落库 |
| POST | `/api/tenants/import` | 开始导入，返回任务 ID |

### 10.4 备份与恢复

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/tenants/{id}/backups` | 备份列表 |
| POST | `/api/tenants/{id}/backups` | 立即备份 |
| POST | `/api/tenants/{id}/restore` | 从指定备份恢复，body：`{backup_id}` |
| DELETE | `/api/tenants/{id}/backups/{backup_id}` | 删除备份 |

### 10.5 回收站

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/trash` | 回收站列表 |
| POST | `/api/trash/{tenant_id}/restore` | 恢复租户 |
| DELETE | `/api/trash/{tenant_id}` | 强制删除（跳过剩余保留期） |

### 10.6 升级

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/upgrades` | 当前版本、已上传版本、最近任务 |
| GET | `/api/upgrades/npm` | npm 上可用的版本，标出本机已安装的，并返回实际生效的源 |
| POST | `/api/upgrades/npm/{version}/install` | 从 npm 下载并安装到本机 |
| POST | `/api/upgrades/upload` | 上传二进制（multipart，离线兜底） |
| POST | `/api/upgrades/{version}/pilot` | 试点，body：`{tenant_id}` |
| POST | `/api/upgrades/{version}/apply` | 批量升级 |
| POST | `/api/upgrades/rollback` | 回滚到上一版 |
| GET | `/api/upgrades/self` | 平台自身版本、程序路径、是否支持自升级 |
| POST | `/api/upgrades/self` | 上传平台二进制（multipart），替换并重启平台服务 |

### 10.7 任务、审计、设置

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/jobs/{id}` | 任务进度和明细 |
| GET | `/api/audit` | 审计列表，支持筛选和分页 |
| GET | `/api/settings` | 平台设置 |
| PATCH | `/api/settings` | 修改设置 |
| POST | `/api/settings/apply-quota` | 把当前默认资源上限下发到所有实例，返回后台任务 |
| GET | `/api/health` | 平台自身健康检查（公开） |

---

## 11. 数据表

### 11.1 `tenants`

```sql
CREATE TABLE tenants (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  username        TEXT NOT NULL UNIQUE,
  display_name    TEXT NOT NULL DEFAULT '',
  uid             INTEGER,
  gid             INTEGER,
  port            INTEGER NOT NULL UNIQUE,
  status          TEXT NOT NULL,
  version         TEXT NOT NULL DEFAULT '',
  cpu_quota       TEXT NOT NULL DEFAULT '',
  memory_max      TEXT NOT NULL DEFAULT '',
  tasks_max       INTEGER NOT NULL DEFAULT 0,
  home_dir        TEXT NOT NULL,
  data_dir        TEXT NOT NULL,
  unit_name       TEXT NOT NULL,
  notes           TEXT NOT NULL DEFAULT '',
  disk_bytes      INTEGER NOT NULL DEFAULT 0,
  disk_checked_at TEXT,
  last_health_at  TEXT,
  last_health_ok  INTEGER NOT NULL DEFAULT 0,
  last_error      TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  deleted_at      TEXT,
  trash_path      TEXT
);
```

### 11.2 `ports`

```sql
CREATE TABLE ports (
  port        INTEGER PRIMARY KEY,
  tenant_id   INTEGER,
  state       TEXT NOT NULL,  -- allocated / reserved / free
  updated_at  TEXT NOT NULL
);
```

### 11.3 `backups`

```sql
CREATE TABLE backups (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  tenant_id    INTEGER NOT NULL,
  username     TEXT NOT NULL,
  created_at   TEXT NOT NULL,
  path         TEXT NOT NULL,
  size_bytes   INTEGER NOT NULL DEFAULT 0,
  status       TEXT NOT NULL,  -- ok / failed
  error        TEXT NOT NULL DEFAULT '',
  integrity_ok INTEGER NOT NULL DEFAULT 0
);
```

### 11.4 `audit_logs`

```sql
CREATE TABLE audit_logs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at  TEXT NOT NULL,
  actor       TEXT NOT NULL,
  action      TEXT NOT NULL,
  tenant_id   INTEGER,
  tenant_name TEXT NOT NULL DEFAULT '',
  detail      TEXT NOT NULL DEFAULT '{}',
  result      TEXT NOT NULL,  -- ok / failed
  error       TEXT NOT NULL DEFAULT '',
  ip          TEXT NOT NULL DEFAULT '',
  user_agent  TEXT NOT NULL DEFAULT ''
);
```

审计里**绝不记录**密码、模型密钥、备份内容。

### 11.5 `admins` / `sessions`

```sql
CREATE TABLE admins (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  username      TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  last_login_at TEXT
);

CREATE TABLE sessions (
  token_hash TEXT PRIMARY KEY,
  admin_id   INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  ip         TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT ''
);
```

### 11.6 `versions` / `jobs` / `job_items`

```sql
CREATE TABLE versions (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  version     TEXT NOT NULL UNIQUE,
  path        TEXT NOT NULL,
  sha256      TEXT NOT NULL,
  size_bytes  INTEGER NOT NULL,
  uploaded_at TEXT NOT NULL,
  status      TEXT NOT NULL  -- installed / current / previous
);

CREATE TABLE jobs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  type        TEXT NOT NULL,      -- import / bulk / upgrade
  status      TEXT NOT NULL,      -- pending / running / succeeded / failed / partial
  total       INTEGER NOT NULL DEFAULT 0,
  done        INTEGER NOT NULL DEFAULT 0,
  failed      INTEGER NOT NULL DEFAULT 0,
  payload     TEXT NOT NULL DEFAULT '{}',
  result      TEXT NOT NULL DEFAULT '{}',
  created_at  TEXT NOT NULL,
  started_at  TEXT,
  finished_at TEXT
);

CREATE TABLE job_items (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id    INTEGER NOT NULL,
  tenant_id INTEGER,
  name      TEXT NOT NULL DEFAULT '',
  status    TEXT NOT NULL,  -- pending / running / ok / failed
  error     TEXT NOT NULL DEFAULT ''
);
```

### 11.7 `settings`

```sql
CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
```

---

## 12. 平台配置

`/etc/clawbench-fleet/config.yaml`：

```yaml
listen: "0.0.0.0:30000"
data_dir: "/var/lib/clawbench-fleet"

clawbench:
  install_dir: "/opt/clawbench-fleet"
  port_start: 21000
  port_end: 29999
  ports_per_tenant: 3
  uid_start: 3000
  uid_end: 9999
  default_cpu_quota: "200%"
  default_memory_max: "4G"
  default_tasks_max: 512
  disk_warn_gb: 5
  npm_package: "@xulongzhe/clawbench"
  npm_registry: ""

backup:
  enabled: true
  hour: 3
  retention_days: 7

trash:
  retention_days: 30

audit:
  retention_days: 180

tls:
  cert_dir: ""   # 留空表示 HTTP；填证书目录则启用 HTTPS
```

管理员密码不在配置文件里，存在数据库；首次启动生成随机密码，写到 `/var/lib/clawbench-fleet/initial-password`（0600）并打印到终端。

---

## 13. 安全设计

### 13.1 平台自身

- 以 root 运行，但只做必要的系统操作；不把 root 能力暴露给租户。
- 管理界面默认监听 `0.0.0.0:30000`，部署文档要求用防火墙只放行管理员网段。
- 支持配置 TLS 证书目录；没有证书时是 HTTP，密码和会话在网络上明文传输，公网部署必须配 TLS。
- 管理员密码用 bcrypt 存储；登录失败按 IP 限速，连续失败锁定。
- 会话 Cookie：`HttpOnly`、`SameSite=Strict`；启用 HTTPS 时加 `Secure`。
- 所有写操作校验 `Origin`，防跨站请求伪造。
- 会话存数据库，支持退出和过期清理。

### 13.2 命令执行

- 只用 `exec.CommandContext` 直接调用固定程序（`useradd`、`userdel`、`systemctl`、`journalctl`、`chown`、`du`），参数用数组传，**绝不拼 shell 字符串**。
- 登录名先过白名单正则，再拼路径；所有路径用 `filepath.Join` 后校验前缀，防目录穿越。
- systemd 单元里的变量只允许：校验过的登录名、整数端口、校验过的配额字符串（如 `200%`、`4G`）。
- 任何用户输入都不允许包含换行，防止往单元文件里注入指令。

### 13.3 文件权限

- 平台数据库、备份、回收站、上传目录：root 0600/0700。
- 租户家目录：租户 0700，平台建完后 `chown` 给租户。
- 租户配置文件：租户 0600，里面有密码哈希和模型密钥。
- 升级二进制：root 0755，租户只能执行不能改。

### 13.4 审计

- 所有写操作记录：时间、管理员、动作、对象、结果、IP、User-Agent。
- 危险操作额外记录参数摘要（例如恢复了哪个备份、升级到哪个版本）。
- 审计只追加，不提供修改接口；保留 180 天。
- 日志查看、备份下载也记审计。

### 13.5 租户隔离

- 每个租户一个 Linux 用户，家目录 0700。
- systemd 单元用 `User=` 降权，`NoNewPrivileges=true`。
- CPU、内存、进程数用 cgroup 限制。
- 不做网络隔离；租户之间可以互相访问端口，但需要密码。
- 再次强调：这是防误操作和资源抢占，不是防恶意租户。
- 纳管进来的老账号保持原样：平台不会改它的 shell、密码和登录能力，只接管服务。`root` 纳管后依然是 root。

---

## 14. 失败处理清单

| 场景 | 平台行为 |
|---|---|
| 登录名已存在 | 拒绝，提示重名 |
| 端口不够 | 拒绝新建/导入，提示剩余容量 |
| UID 不够 | 拒绝新建，提示调整 UID 范围 |
| `useradd` 失败 | 回滚已创建的目录和端口，返回系统错误 |
| systemd 不可用 | 启动时自检失败，平台拒绝启动并说明原因 |
| 服务起不来 | 状态 `error`，详情页展示 journal 最后 100 行 |
| 健康检查超时 | 状态 `error`，不自动删数据；提供“重启/修复” |
| 写配置失败 | 用临时文件 + 原子替换；失败不覆盖原文件 |
| 备份失败 | 标记失败，保留上一次成功备份；总览页警告 |
| 备份磁盘不足 | 暂停当天备份，警告；不删旧备份 |
| 恢复时备份损坏 | 拒绝恢复，不动当前数据 |
| 升级包不是可执行文件 | 上传阶段拒绝 |
| 升级试点失败 | 切回旧版本，重启试点，标记失败 |
| 批量升级中途失败 | 停止后续，回滚已升级的，报告失败项 |
| 删除时服务停不下来 | 先 `systemctl stop`，超时后强制 `kill` 进程组，再继续 |
| 删除时家目录移不走 | 中止删除，保持原状，报错 |
| 同一租户并发操作 | 每租户一把锁，后到的返回 409 |
| 平台崩溃后重启 | 对账 systemd 单元和数据库，标记异常/孤儿 |
| 服务器重启 | systemd 自动拉起平台和所有已启用的租户 |

---

## 15. 部署

### 15.1 环境要求

- Linux，systemd，cgroup v2。
- 推荐 Ubuntu 22.04+ / Debian 12+。
- x86_64 或 arm64。
- root 权限。
- Node.js 22+ 和 npm（Clawbench 的 ACP 适配器需要）。
- git、bash、curl、ca-certificates。

### 15.2 安装

1. 把 `clawbench-fleet` 二进制放到 `/usr/local/bin/`。
2. 创建目录：`/etc/clawbench-fleet`、`/var/lib/clawbench-fleet`、`/opt/clawbench-fleet/versions`。
3. 写 `/etc/clawbench-fleet/config.yaml`。
4. 写 `/etc/systemd/system/clawbench-fleet.service`：
   ```ini
   [Unit]
   Description=Clawbench Fleet
   After=network-online.target
   Wants=network-online.target

   [Service]
   Type=simple
   User=root
   ExecStart=/usr/local/bin/clawbench-fleet --config /etc/clawbench-fleet/config.yaml
   Restart=always
   RestartSec=3

   [Install]
   WantedBy=multi-user.target
   ```
5. `systemctl daemon-reload && systemctl enable --now clawbench-fleet`。
6. 从 `/var/lib/clawbench-fleet/initial-password` 读初始密码，打开 `http://<IP>:30000` 登录。
7. 在升级中心装第一份 Clawbench（能连 npm 就点「从 npm 安装」，否则上传二进制），才能开始建号。

### 15.3 卸载

- 停平台服务，保留数据目录。
- 不会自动删除租户账号和家目录；需要管理员手动清理。
- 提供 `--purge` 脚本选项才删除平台数据（不删租户）。

---

## 16. 测试策略

### 16.1 后端单元测试

- 用假的命令执行器（记录调用了什么命令、返回预设结果），不需要 root 就能测：
  - 登录名校验、保留名、重名。
  - 端口分配：连续 3 个、回收站占用、范围耗尽。
  - 密码生成与哈希格式。
  - 配置文件生成与原子替换。
  - systemd 单元生成（字段、转义、无注入）。
  - CSV 解析、预览、冲突检测。
  - 备份保留策略、恢复校验。
  - 审计写入与脱敏。
  - 升级状态机、失败回滚。
- 用临时 SQLite 测数据层。

### 16.2 集成测试

- 在一次性 Linux 虚拟机或带 systemd 的容器里：
  - 用一个假 Clawbench（小 HTTP 服务）验证建号、启停、健康检查、删除。
  - 验证 cgroup 限制生效。
- 不在开发机上直接跑 root 集成测试。

### 16.3 前端测试

- Vitest 测关键工具函数和状态管理。
- 关键页面做组件测试：登录、租户列表、导入预览、升级进度。

### 16.4 验收清单

- [ ] 新建租户后 60 秒内能打开 `IP:端口` 并登录。
- [ ] 停止后打不开，启动后恢复。
- [ ] 重置密码后旧密码失效、新密码可登录。
- [ ] 重启服务器后，平台和已启用租户自动起来。
- [ ] CSV 导入 50 条，成功的能登录，失败的清单可下载。
- [ ] 删除租户后家目录进回收站，30 天内能恢复。
- [ ] 备份后删除数据库，能从备份恢复。
- [ ] 升级试点失败能自动回滚。
- [ ] 批量升级中途失败能停止并回滚。
- [ ] 审计能看到所有写操作，且没有密码/密钥。
- [ ] 开了 SSH 的租户能用网页同一个密码 SSH 登录，关掉的不能；所有租户都不能 sudo。

---

## 17. 里程碑

### M1：骨架能跑通

- 平台登录、SQLite、租户表。
- 新建单个租户、删除租户。
- systemd 单元生成、启动/停止/重启。
- 健康检查、状态展示。
- 验收：能在真实服务器上建一个号并打开。

### M2：日常运维

- 密码重置。
- 日志查看。
- 配额修改。
- 用量看板（CPU/内存/进程/磁盘缓存）。
- 批量启停/重启。
- 验收：管理员不用登服务器就能完成日常运维。

### M3：数据安全

- 每天自动备份、手动备份。
- 恢复。
- 回收站、恢复租户、彻底删除。
- 审计日志页面。
- 验收：误删能恢复，数据库坏了能从备份救回。

### M4：版本升级

- 上传二进制、版本列表。
- 试点、批量升级、回滚。
- 任务进度页面。
- 验收：坏版本能在试点阶段被拦住，已升级的能回滚。

### M5：打磨

- CSV 预览和结果清单。
- 安装脚本、systemd 单元、部署文档。
- 错误提示、空状态、加载状态。
- 单元测试补齐。
- 验收：按文档从零部署一台新服务器，30 分钟内跑通全流程。

---

## 18. 待确认

下面这些是我先按推荐值写进文档的，开工前请确认：

1. 项目名用 `clawbench-fleet`，目录 `/home/xulongzhe/projects/clawbench-fleet`。
2. 管理端口 30000，租户端口 21000–29999，每人 3 个。
3. 备份保留 7 天。
4. 界面第一版只做中文。
5. 默认关闭租户的端口转发（`port_forward.enabled: false`），需要时管理员在租户详情里打开。
6. 回收站提供“恢复租户”按钮（推荐提供）。
7. 第一版不做 HTTPS，公网部署时由前面的反向代理负责；平台预留证书目录配置。
8. 接管已经在跑的实例：通过“扫描已有实例”手动勾选纳管，不做自动发现；纳管会把服务改成平台统一管理（重启一次）。

---

## 19. 附：和 Clawbench 本体的接口约定

平台依赖 Clawbench 的这几个稳定行为：

| 行为 | 说明 |
|---|---|
| `--port` 覆盖配置端口 | 保证员工改配置也改不了对外端口 |
| `--data-dir` 指定数据目录 | 每个租户一个目录，避免实例锁冲突 |
| `--managed` | 关闭实例自升级和自重启 |
| `GET /api/health` | 公开，返回 `app=clawbench`、`version`、`protocol`、`pid` |
| 配置路径 `<data-dir>/config/config.yaml` | 平台写端口、密码、端口转发开关 |
| 密码格式 `sha256:<hex>` | 哈希算法 `sha256(密码 + "clawbench-salt")` |
| 数据库 `<data-dir>/ClawBench.db` | 备份和恢复的对象 |
| 一个数据目录只能跑一个实例 | 每租户独立目录，天然满足 |

如果 Clawbench 以后改了其中任何一条，平台需要同步改；建议在 Clawbench 仓库加一个契约测试，钉住这些行为。
