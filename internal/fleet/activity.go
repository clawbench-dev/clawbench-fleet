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

// 最近活跃时间的四个来源，按“离现在最近”取最大值：
//   - chat_history：每条对话消息的时间，最基本的依据；
//   - chat_tool_calls：工具调用的时间，比消息时间更靠后，能反映一次还在进行的对话；
//   - chat_metadata：每条消息的元数据（模型、耗时、花费），相当于“这条消息真的跑完了”；
//   - chat_sessions：会话的最后更新时间，兜底。
//
// 这几张表在老版本里不一定存在，查询失败就跳过，不影响其他来源。
var activityQueries = []string{
	`SELECT MAX(created_at) FROM chat_history`,
	`SELECT MAX(created_at) FROM chat_tool_calls`,
	`SELECT MAX(created_at) FROM chat_metadata`,
	`SELECT MAX(updated_at) FROM chat_sessions WHERE removed=0`,
}

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

	var newest messageTime
	for _, query := range activityQueries {
		queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		var raw sql.NullString
		err := db.QueryRowContext(queryCtx, query).Scan(&raw)
		cancel()
		if err != nil || !raw.Valid {
			continue
		}
		if parsed := parseClawbenchTime(raw.String); parsed.ok && parsed.at.After(newest.at) {
			newest = parsed
		}
	}
	return newest, true
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
