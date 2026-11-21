package httpapi

import (
	"net/http"
	"testing"
)

// 这些用例专门走“参数/编号错误、目标不存在”的分支：真实前端也天天会打到
// 这些路径（页面刷新时 id 已经删掉、手输地址等），必须返回明确的 400/404。

func TestTenantActionErrors(t *testing.T) {
	c, _ := newTestClient(t)
	for _, action := range []string{"start", "stop", "restart", "reset-password", "repair"} {
		if status, _ := c.request(http.MethodPost, "/api/tenants/bad/"+action, nil); status != http.StatusBadRequest {
			t.Fatalf("%s with bad id = %d", action, status)
		}
		if status, _ := c.request(http.MethodPost, "/api/tenants/999/"+action, nil); status != http.StatusNotFound {
			t.Fatalf("%s on missing tenant = %d", action, status)
		}
	}
	for _, path := range []string{"logs", "metrics", "backups"} {
		if status, _ := c.request(http.MethodGet, "/api/tenants/bad/"+path, nil); status != http.StatusBadRequest {
			t.Fatalf("%s with bad id = %d", path, status)
		}
		if status, _ := c.request(http.MethodGet, "/api/tenants/999/"+path, nil); status != http.StatusNotFound {
			t.Fatalf("%s on missing tenant = %d", path, status)
		}
	}
}

func TestBackupErrors(t *testing.T) {
	c, _ := newTestClient(t)
	created := createTenant(t, c, "alice")
	id := tenantID(t, created)
	waitForStatus(t, c, id)
	// A tenant that is running has a config directory, so a backup succeeds;
	// the point here is that the handler streams a JSON body, not a crash.
	var backup map[string]any
	if status := c.json(http.MethodPost, "/api/tenants/"+itoa(id)+"/backups", nil, &backup); status != http.StatusCreated {
		t.Fatalf("backup = %d %+v", status, backup)
	}
	if status, _ := c.request(http.MethodGet, "/api/tenants/bad/backups", nil); status != http.StatusBadRequest {
		t.Fatalf("backups with bad id = %d", status)
	}
}

func TestResetAndRepairWarnings(t *testing.T) {
	c, _ := newTestClient(t)
	created := createTenant(t, c, "alice")
	id := tenantID(t, created)
	waitForStatus(t, c, id)
	var repaired map[string]any
	if status := c.json(http.MethodPost, "/api/tenants/"+itoa(id)+"/repair", nil, &repaired); status != http.StatusOK {
		t.Fatalf("repair = %d %+v", status, repaired)
	}
}
