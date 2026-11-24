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
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	UID         int    `json:"uid"`
	GID         int    `json:"gid"`
	Port        int    `json:"port"`
	Status      string `json:"status"`
	Version     string `json:"version"`
	CPUQuota    string `json:"cpu_quota"`
	MemoryMax   string `json:"memory_max"`
	TasksMax    int    `json:"tasks_max"`
	HomeDir     string `json:"home_dir"`
	DataDir     string `json:"data_dir"`
	UnitName    string `json:"unit_name"`
	Notes       string `json:"notes"`
	// SSHEnabled says whether the account has a login shell and a usable
	// password. The password itself is never stored here: only the Linux
	// password hash exists, and the plaintext is shown once at creation or
	// reset.
	SSHEnabled bool  `json:"ssh_enabled"`
	DiskBytes  int64 `json:"disk_bytes"`
	// DiskQuotaGB is the hard limit for the home directory. Zero means no
	// limit. Going over it stops the instance and suspends SSH access.
	DiskQuotaGB  int    `json:"disk_quota_gb"`
	DiskChecked  string `json:"disk_checked_at"`
	LastHealthAt string `json:"last_health_at"`
	LastHealthOK bool   `json:"last_health_ok"`
	// LastActiveAt is the newest timestamp of the员工 actually talking to the
	// instance, read from the instance's own database. Empty means unknown.
	LastActiveAt string `json:"last_active_at"`
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
// Address builds the URL an employee opens. The instance always speaks HTTPS
// once the platform ships it a certificate, so the scheme is fixed; only the
// host varies — see Service.Address.
func (t Tenant) Address(host string) string {
	if host == "" {
		host = "127.0.0.1"
	}
	return "https://" + host + ":" + strconv.Itoa(t.Port)
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
	// DefaultDiskQuotaGB applies to new tenants; zero means no limit.
	DefaultDiskQuotaGB int `json:"default_disk_quota_gb"`
	// MetricsRetention controls how long the usage history is kept.
	MetricsRetention int    `json:"metrics_retention_days"`
	BackupEnabled    bool   `json:"backup_enabled"`
	BackupHour       int    `json:"backup_hour"`
	BackupRetention  int    `json:"backup_retention_days"`
	TrashRetention   int    `json:"trash_retention_days"`
	AuditRetention   int    `json:"audit_retention_days"`
	AllowPortForward bool   `json:"allow_port_forward"`
	PublicHost       string `json:"public_host"`
	GuestEnabled     bool   `json:"guest_enabled"`
	GuestNotice      string `json:"guest_notice"`
	GuestHelp        string `json:"guest_help"`
	// SSHEnabled is the default applied to newly created tenants.
	SSHEnabled bool `json:"ssh_enabled"`
	// TLSEnabled turns HTTPS on for every instance the platform manages.
	TLSEnabled bool `json:"tls_enabled"`
	// TLSCertDir is where the shared certificate lives. Empty means
	// "<data_dir>/certs", and the platform generates a self-signed pair there.
	TLSCertDir string `json:"tls_cert_dir"`
}

// ConfigEntry is one key/value pair written into a tenant's config.yaml. Key
// uses dotted notation ("tls.cert_dir") and Value is stored as a string; scalar
// values are written as YAML scalars, not quoted strings.
type ConfigEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type Metrics struct {
	CPUPercent  float64 `json:"cpu_percent"`
	MemoryBytes int64   `json:"memory_bytes"`
	PIDs        int     `json:"pids"`
	DiskBytes   int64   `json:"disk_bytes"`
	SampledAt   string  `json:"sampled_at"`
}

// MetricSample is one point in a tenant's usage history. Samples are taken
// every few minutes so the detail page can draw a trend without keeping the
// per-second values.
type MetricSample struct {
	At          string  `json:"at"`
	CPUPercent  float64 `json:"cpu_percent"`
	MemoryBytes int64   `json:"memory_bytes"`
	PIDs        int     `json:"pids"`
	DiskBytes   int64   `json:"disk_bytes"`
}

// GuestReport is a "my instance won't open" note left from the public page.
type GuestReport struct {
	ID         int64  `json:"id"`
	TenantID   int64  `json:"tenant_id"`
	TenantName string `json:"tenant_name"`
	Message    string `json:"message"`
	CreatedAt  string `json:"created_at"`
	IP         string `json:"ip"`
	HandledAt  string `json:"handled_at"`
	HandledBy  string `json:"handled_by"`
}

type HealthInfo struct {
	App      string `json:"app"`
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
	PID      int    `json:"pid"`
}
