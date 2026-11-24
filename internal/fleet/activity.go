package fleet

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clawbench-fleet/internal/domain"

	// The instance databases are SQLite too; importing the driver here keeps
	// activity.go from depending on the store package just to open them.
	_ "modernc.org/sqlite"
)

// 员工到底还在不在用这个实例，平台是看不出来的：实例一直在跑、健康检查也一直
// 通过，不代表有人继续在里面干活。唯一的证据在实例自己的数据库里：最近一条
// 对话消息的时间，就是这个实例最近一次被真正使用的时刻。
//
// 所以这里每天（也可以手动触发）挨个打开实例的数据库，把最近对话的时间读回来，
// 记到平台的 tenants.last_active_at 上。整个过程是只读的：数据库以只读方式打开，
// 不写任何东西，也不会因为读失败而影响实例运行。

// 只看最新一条对话消息的时间，这是“最近一次真正有人用”最直接的依据。会话表
// 的更新时间不算：它只在改标题、归档这类操作时动，一个人连续聊两小时可能一动不动。
// 归档的会话也不特殊处理，里面的消息照样算，归档本身就是一次访问。
//
// 老版本的表结构可能不同，读失败就当这个实例没有记录，不影响其他实例。
const activityQuery = `SELECT MAX(created_at) FROM chat_history`

// 实例数据库可能存的几种时间写法，见 clawbenchTimeLayouts。
var clawbenchTimeLayouts = []string{
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05Z",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

type messageTime struct {
	at time.Time
	ok bool
}

// parseClawbenchTime 把实例数据库里读出来的时间字符串转成时间。SQLite 的
// CURRENT_TIMESTAMP 存的是 UTC，但字符串里不写时区，所以不带时区的写法按 UTC 处理。
func parseClawbenchTime(raw string) messageTime {
	if raw == "" {
		return messageTime{}
	}
	for _, layout := range clawbenchTimeLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			if !hasZone(layout) {
				t = t.UTC()
			}
			return messageTime{at: t, ok: true}
		}
	}
	return messageTime{}
}

func hasZone(layout string) bool {
	return strings.Contains(layout, "Z07:00") || strings.Contains(layout, "-07:00") ||
		strings.HasSuffix(layout, "Z")
}

// tenantLastActive 返回一个租户实例里的最近对话时间。hasDB 表示找到并打开了
// 实例数据库；找不到数据库时返回零值，调用方不要据此把已有记录清零。
func (s *Service) tenantLastActive(ctx context.Context, t domain.Tenant) (messageTime, bool) {
	dbPath := filepath.Join(t.DataDir, "ClawBench.db")
	if _, err := os.Stat(dbPath); err != nil {
		return messageTime{}, false
	}
	// 只读 + 不建库：实例创建到一半、数据库还没建好时，绝不能顺手造出一个空库来。
	dsn := "file:" + dbPath + "?mode=ro&_pragma=busy_timeout(3000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return messageTime{}, false
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	var raw sql.NullString
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.QueryRowContext(queryCtx, activityQuery).Scan(&raw); err != nil || !raw.Valid {
		return messageTime{}, true
	}
	return parseClawbenchTime(raw.String), true
}

// RefreshLastActive 遍历所有没被删除的租户，更新最近活跃时间。单个租户读失败
// 只跳过它自己，不影响别人。
func (s *Service) RefreshLastActive(ctx context.Context) {
	tenants, err := s.allTenants()
	if err != nil {
		return
	}
	for _, t := range tenants {
		if t.Status == domain.StatusTrashed || t.Status == domain.StatusDeleting {
			continue
		}
		found, hasDB := s.tenantLastActive(ctx, t)
		if !hasDB || !found.ok {
			continue
		}
		at := found.at.Format(time.RFC3339)
		if at == t.LastActiveAt {
			continue
		}
		_ = s.Store.SetTenantLastActive(t.ID, at)
	}
}

// RefreshTenantLastActive 只刷新一个租户，供详情页在数据为空时按需补一次。
func (s *Service) RefreshTenantLastActive(ctx context.Context, t *domain.Tenant) {
	found, hasDB := s.tenantLastActive(ctx, *t)
	if !hasDB || !found.ok {
		return
	}
	at := found.at.Format(time.RFC3339)
	if at == t.LastActiveAt {
		return
	}
	if err := s.Store.SetTenantLastActive(t.ID, at); err != nil {
		return
	}
	t.LastActiveAt = at
}
