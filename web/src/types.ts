export type TenantStatus =
  | 'creating'
  | 'running'
  | 'stopped'
  | 'error'
  | 'deleting'
  | 'trashed'

export interface Metrics {
  cpu_percent: number
  memory_bytes: number
  pids: number
  disk_bytes: number
  sampled_at: string
}

export interface Tenant {
  id: number
  username: string
  display_name: string
  uid: number
  gid: number
  port: number
  status: TenantStatus | string
  version: string
  cpu_quota: string
  memory_max: string
  tasks_max: number
  home_dir: string
  data_dir: string
  unit_name: string
  notes: string
  disk_bytes: number
  disk_checked_at: string
  last_health_at: string
  last_health_ok: boolean
  last_active_at: string
  last_error: string
  created_at: string
  updated_at: string
  deleted_at?: string
  trash_path?: string
  address: string
  metrics: Metrics
  protected: boolean
}

export interface AdoptCandidate {
  username: string
  display_name: string
  uid: number
  gid: number
  home_dir: string
  data_dir: string
  port: number
  port_source: string
  unit_name: string
  unit_exists: boolean
  unit_active: boolean
  service_active: boolean
  health_ok: boolean
  version: string
  cpu_quota: string
  memory_max: string
  tasks_max: number
  already_managed: boolean
  protected: boolean
  status: string
  blocked: string
  warnings: string[]
}

export interface HostStats {
  cpu_percent: number
  mem_total: number
  mem_used: number
  disk_total: number
  disk_free: number
  load1: number
}

export interface Backup {
  id: number
  tenant_id: number
  username: string
  created_at: string
  path: string
  size_bytes: number
  status: string
  error: string
  integrity_ok: boolean
}

export interface AuditEntry {
  id: number
  created_at: string
  actor: string
  action: string
  tenant_id?: number
  tenant_name: string
  detail: string
  result: string
  error: string
  ip: string
  user_agent: string
}

export interface JobItem {
  id: number
  job_id: number
  tenant_id?: number
  name: string
  status: string
  error: string
}

export interface Job {
  id: number
  type: string
  status: string
  total: number
  done: number
  failed: number
  payload: string
  result: string
  created_at: string
  started_at: string
  finished_at: string
  items?: JobItem[]
}

export interface Version {
  id: number
  version: string
  path: string
  sha256: string
  size_bytes: number
  uploaded_at: string
  status: string
}

export interface NpmVersion {
  version: string
  installed: boolean
}

export interface Settings {
  port_start: number
  port_end: number
  ports_per_tenant: number
  uid_start: number
  uid_end: number
  default_cpu_quota: string
  default_memory_max: string
  default_tasks_max: number
  disk_warn_gb: number
  backup_enabled: boolean
  backup_hour: number
  backup_retention_days: number
  trash_retention_days: number
  audit_retention_days: number
  allow_port_forward: boolean
  public_host: string
  guest_enabled: boolean
  guest_notice: string
  guest_help: string
}

export interface Overview {
  host: HostStats
  counts: Record<string, number>
  total: number
  anomalies: Tenant[]
  disk_warnings: Tenant[]
  recent_audit: AuditEntry[]
  recent_jobs: Job[]
  current_version: string
  orphans: string[]
  mode: string
  backup_enabled: boolean
  open_reports: number
  guest_reports: GuestReport[]
}

export interface GuestReport {
  id: number
  tenant_id: number
  tenant_name: string
  message: string
  created_at: string
  ip: string
  handled_at: string
  handled_by: string
}

export interface GuestInstance {
  id: number
  username: string
  display_name: string
  port: number
  address: string
  status: string
  disk_warn: boolean
}

export interface GuestPage {
  enabled: boolean
  notice: string
  help: string
  items: GuestInstance[]
}

export interface TrashItem extends Tenant {
  days_left: number
}

export interface ImportRow {
  row: number
  username: string
  display_name: string
  notes: string
  error: string
}

export interface Admin {
  id: number
  username: string
  created_at: string
  last_login_at: string
}

export interface Page<T> {
  items: T[]
  total: number
  page: number
  page_size: number
}
