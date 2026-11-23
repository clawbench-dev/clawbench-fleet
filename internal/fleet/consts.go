package fleet

// Shared string values that appear in audit records, backup rows and API
// payloads. Keeping them here stops the linter (and future readers) from
// wondering whether two identical literals mean the same thing.
const (
	statusOK     = "ok"
	statusFailed = "failed"
	actorSystem  = "system"
	fieldPort    = "port"
	fieldUID     = "uid"
	fieldVersion = "version"
	fieldDone    = "done"

	// 以下是“默认配额”功能（另一个改动）用到的，先保留以便那部分继续编译。
	fieldCPUQuota  = "cpu_quota"
	fieldMemoryMax = "memory_max"
	fieldTasksMax  = "tasks_max"

	auditTenantUpdate = "tenant.update"
)
