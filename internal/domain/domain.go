package domain

import "strconv"

// Tenant status values. These are persisted in SQLite and rendered by the UI,
// so treat them as a wire contract.
const (
	StatusCreating = "creating"
	StatusRunning  = "running"
	StatusStopped  = "stopped"
	StatusError    = "error"
	StatusDeleting = "deleting"
	StatusTrashed  = "trashed"
)

// Tenant is one Linux account plus one Clawbench instance.
type Tenant struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	UID          int    `json:"uid"`
	GID          int    `json:"gid"`
	Port         int    `json:"port"`
	Status       string `json:"status"`
	Version      string `json:"version"`
	CPUQuota     string `json:"cpu_quota"`
	MemoryMax    string `json:"memory_max"`
	TasksMax     int    `json:"tasks_max"`
	HomeDir      string `json:"home_dir"`
	DataDir      string `json:"data_dir"`
	UnitName     string `json:"unit_name"`
	Notes        string `json:"notes"`
	DiskBytes    int64  `json:"disk_bytes"`
	DiskChecked  string `json:"disk_checked_at"`
	LastHealthAt string `json:"last_health_at"`
	LastHealthOK bool   `json:"last_health_ok"`
	LastError    string `json:"last_error"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	DeletedAt    string `json:"deleted_at,omitempty"`
	TrashPath    string `json:"trash_path,omitempty"`
}

// ProtectedUsername reports whether an account must never be deleted. root
// owns the machine itself, so removing it is never a valid operation.
func ProtectedUsername(username string) bool { return username == "root" }

// Address is what the admin copies and sends to the employee.
func (t Tenant) Address(host string) string {
	if host == "" {
		host = "127.0.0.1"
	}
	return "http://" + host + ":" + strconv.Itoa(t.Port)
}

type Backup struct {
	ID          int64  `json:"id"`
	TenantID    int64  `json:"tenant_id"`
	Username    string `json:"username"`
	CreatedAt   string `json:"created_at"`
	Path        string `json:"path"`
	SizeBytes   int64  `json:"size_bytes"`
	Status      string `json:"status"`
	Error       string `json:"error"`
	IntegrityOK bool   `json:"integrity_ok"`
}

type AuditEntry struct {
	ID         int64  `json:"id"`
	CreatedAt  string `json:"created_at"`
	Actor      string `json:"actor"`
	Action     string `json:"action"`
	TenantID   int64  `json:"tenant_id,omitempty"`
	TenantName string `json:"tenant_name"`
	Detail     string `json:"detail"`
	Result     string `json:"result"`
	Error      string `json:"error"`
	IP         string `json:"ip"`
	UserAgent  string `json:"user_agent"`
}

type Job struct {
	ID         int64     `json:"id"`
	Type       string    `json:"type"`
	Status     string    `json:"status"`
	Total      int       `json:"total"`
	Done       int       `json:"done"`
	Failed     int       `json:"failed"`
	Payload    string    `json:"payload"`
	Result     string    `json:"result"`
	CreatedAt  string    `json:"created_at"`
	StartedAt  string    `json:"started_at"`
	FinishedAt string    `json:"finished_at"`
	Items      []JobItem `json:"items,omitempty"`
}

type JobItem struct {
	ID       int64  `json:"id"`
	JobID    int64  `json:"job_id"`
	TenantID int64  `json:"tenant_id,omitempty"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Error    string `json:"error"`
}

const (
	JobPending   = "pending"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobPartial   = "partial"
)

type Version struct {
	ID         int64  `json:"id"`
	Version    string `json:"version"`
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"size_bytes"`
	UploadedAt string `json:"uploaded_at"`
	Status     string `json:"status"`
}

type Admin struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	LastLoginAt  string `json:"last_login_at"`
}

type Session struct {
	TokenHash string
	AdminID   int64
	CreatedAt string
	ExpiresAt string
	IP        string
	UserAgent string
}

// Settings are platform-wide knobs edited from the UI. Values that affect
// allocation (ports, UIDs) only apply to tenants created afterwards.
type Settings struct {
	PortStart        int    `json:"port_start"`
	PortEnd          int    `json:"port_end"`
	PortsPerTenant   int    `json:"ports_per_tenant"`
	UIDStart         int    `json:"uid_start"`
	UIDEnd           int    `json:"uid_end"`
	DefaultCPUQuota  string `json:"default_cpu_quota"`
	DefaultMemoryMax string `json:"default_memory_max"`
	DefaultTasksMax  int    `json:"default_tasks_max"`
	DiskWarnGB       int    `json:"disk_warn_gb"`
	BackupEnabled    bool   `json:"backup_enabled"`
	BackupHour       int    `json:"backup_hour"`
	BackupRetention  int    `json:"backup_retention_days"`
	TrashRetention   int    `json:"trash_retention_days"`
	AuditRetention   int    `json:"audit_retention_days"`
	AllowPortForward bool   `json:"allow_port_forward"`
	PublicHost       string `json:"public_host"`
}

type Metrics struct {
	CPUPercent  float64 `json:"cpu_percent"`
	MemoryBytes int64   `json:"memory_bytes"`
	PIDs        int     `json:"pids"`
	DiskBytes   int64   `json:"disk_bytes"`
	SampledAt   string  `json:"sampled_at"`
}

type HealthInfo struct {
	App      string `json:"app"`
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
	PID      int    `json:"pid"`
}
