package fleet

import "time"

// Shared string values that appear in audit records, backup rows and API
// payloads. Keeping them here stops the linter (and future readers) from
// wondering whether two identical literals mean the same thing.
const (
	statusOK       = "ok"
	statusFailed   = "failed"
	actorSystem    = "system"
	fieldPort      = "port"
	fieldUID       = "uid"
	fieldVersion   = "version"
	fieldDone      = "done"
	fieldCPUQuota  = "cpu_quota"
	fieldMemoryMax = "memory_max"
	fieldTasksMax  = "tasks_max"

	auditTenantUpdate = "tenant.update"

	// modeDev is the simulated backend's mode string. Several flows behave
	// differently when nothing real is installed.
	modeDev = "dev"

	// bulkResetPasswordAction resets passwords for a whole selection and puts
	// the new ones in a downloadable file.
	bulkResetPasswordAction = "reset-password"

	// sampleInterval is how often one usage-history point is written. Frequent
	// enough to see a spike after the fact, cheap enough to keep for a week.
	sampleInterval = 5 * time.Minute
)
