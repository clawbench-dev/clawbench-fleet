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
)
