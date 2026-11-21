package fleet

import (
	"clawbench-fleet/internal/store"
)

// 测试里只用到这几个 store 值；集中在这里，避免每个测试文件都导入 store。
func storePortExhausted() error             { return store.ErrPortExhausted }
func storeTenantFilter() store.TenantFilter { return store.TenantFilter{} }
func storeAuditFilter() store.AuditFilter   { return store.AuditFilter{} }
