export function formatBytes(n: number): string {
  if (!n || n < 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

export function formatTime(value: string): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  return d.toLocaleString('zh-CN', { hour12: false })
}

export function formatDate(value: string): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  return d.toLocaleDateString('zh-CN')
}

// formatAgo 把“最近活跃时间”说成人话：管理员要看的是“这个人多久没动了”，
// 不是精确到秒的时间戳。超过 30 天直接给日期，再精确到天就没意义了。
export function formatAgo(value: string, now: Date = new Date()): string {
  if (!value) return '无记录'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  const minutes = Math.floor((now.getTime() - d.getTime()) / 60000)
  if (minutes < 0) return formatDate(value)
  if (minutes < 1) return '刚刚'
  if (minutes < 60) return `${minutes} 分钟前`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} 小时前`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days} 天前`
  return formatDate(value)
}

export function formatPercent(n: number): string {
  if (!Number.isFinite(n)) return '0%'
  return `${n.toFixed(n >= 10 ? 0 : 1)}%`
}

export function shortSha(sha: string): string {
  return sha ? sha.slice(0, 12) : '—'
}

export function statusLabel(status: string): string {
  const map: Record<string, string> = {
    running: '运行中',
    stopped: '已停止',
    error: '异常',
    creating: '创建中',
    deleting: '删除中',
    trashed: '回收站',
  }
  return map[status] || status
}

export function actionLabel(action: string): string {
  const map: Record<string, string> = {
    'auth.login': '登录',
    'auth.password': '修改管理员密码',
    'tenant.create': '新建租户',
    'tenant.delete': '删除租户',
    'tenant.start': '启动',
    'tenant.stop': '停止',
    'tenant.restart': '重启',
    'tenant.reset_password': '重置密码',
    'tenant.repair': '修复',
    'tenant.update': '修改配置',
    'tenant.logs': '查看日志',
    'tenant.import': '批量导入',
    'tenant.adopt': '纳入已有实例',
    'tenant.restore': '恢复租户',
    'tenant.purge': '强制删除',
    'backup.create': '创建备份',
    'backup.restore': '恢复备份',
    'backup.delete': '删除备份',
    'backup.download': '下载备份',
    'upgrade.upload': '上传版本',
    'upgrade.npm_install': '从 npm 安装版本',
    'upgrade.pilot': '试点升级',
    'upgrade.apply': '批量升级',
    'upgrade.rollback': '回滚版本',
    'upgrade.restart': '升级重启',
    'settings.update': '修改设置',
  }
  return map[action] || action
}

export function isRunning(status: string): boolean {
  return status === 'running'
}
