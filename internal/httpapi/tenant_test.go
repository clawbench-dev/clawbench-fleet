package httpapi

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"clawbench-fleet/internal/domain"
)

func createTenant(t *testing.T, c *client, username string) map[string]any {
	t.Helper()
	var out map[string]any
	if status := c.json(http.MethodPost, "/api/tenants", map[string]any{
		"username": username, "display_name": "Test " + username,
	}, &out); status != http.StatusCreated {
		t.Fatalf("create %s = %d %v", username, status, out)
	}
	return out
}

func tenantID(t *testing.T, created map[string]any) int64 {
	t.Helper()
	tenant, ok := created["tenant"].(map[string]any)
	if !ok {
		t.Fatalf("no tenant in %v", created)
	}
	id, ok := tenant["id"].(float64)
	if !ok {
		t.Fatalf("no id in %v", tenant)
	}
	return int64(id)
}

func waitForStatus(t *testing.T, c *client, id int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var out struct {
			Tenant struct {
				Status string `json:"status"`
			} `json:"tenant"`
		}
		if status := c.json(http.MethodGet, "/api/tenants/"+itoa(id), nil, &out); status == http.StatusOK && out.Tenant.Status == domain.StatusRunning {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("tenant %d never reached running", id)
}

func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}

func TestTenantLifecycleOverHTTP(t *testing.T) {
	c, _ := newTestClient(t)
	created := createTenant(t, c, "alice")
	if created["password"] == "" {
		t.Fatal("create should return the generated password")
	}
	id := tenantID(t, created)
	waitForStatus(t, c, id)

	var list struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if status := c.json(http.MethodGet, "/api/tenants?q=ali&page=1&page_size=10", nil, &list); status != http.StatusOK {
		t.Fatalf("list = %d", status)
	}
	if list.Total != 1 || len(list.Items) != 1 {
		t.Fatalf("list = %+v", list)
	}

	var detail struct {
		Tenant  map[string]any   `json:"tenant"`
		Backups []map[string]any `json:"backups"`
	}
	if status := c.json(http.MethodGet, "/api/tenants/"+itoa(id), nil, &detail); status != http.StatusOK {
		t.Fatalf("detail = %d", status)
	}
	if detail.Tenant["address"] == "" || detail.Tenant["protected"] != false {
		t.Fatalf("detail payload = %+v", detail.Tenant)
	}

	var updated struct {
		Tenant map[string]any `json:"tenant"`
	}
	if status := c.json(http.MethodPatch, "/api/tenants/"+itoa(id), map[string]any{
		"display_name": "Alice", "cpu_quota": "150%", "memory_max": "2G", "tasks_max": 256,
	}, &updated); status != http.StatusOK {
		t.Fatalf("update = %d", status)
	}
	if updated.Tenant["display_name"] != "Alice" || updated.Tenant["cpu_quota"] != "150%" {
		t.Fatalf("update payload = %+v", updated.Tenant)
	}
	if status := c.json(http.MethodPatch, "/api/tenants/"+itoa(id), map[string]any{"cpu_quota": "lots"}, nil); status != http.StatusBadRequest {
		t.Fatalf("invalid quota = %d, want 400", status)
	}

	for _, action := range []string{"stop", "start", "restart"} {
		if status := c.json(http.MethodPost, "/api/tenants/"+itoa(id)+"/"+action, nil, nil); status != http.StatusOK {
			t.Fatalf("%s = %d", action, status)
		}
	}

	var reset struct {
		Password string `json:"password"`
	}
	if status := c.json(http.MethodPost, "/api/tenants/"+itoa(id)+"/reset-password", nil, &reset); status != http.StatusOK || reset.Password == "" {
		t.Fatalf("reset password = %d %+v", status, reset)
	}

	var logs map[string]any
	if status := c.json(http.MethodGet, "/api/tenants/"+itoa(id)+"/logs?lines=10", nil, &logs); status != http.StatusOK {
		t.Fatalf("logs = %d", status)
	}
	var metrics map[string]any
	if status := c.json(http.MethodGet, "/api/tenants/"+itoa(id)+"/metrics", nil, &metrics); status != http.StatusOK {
		t.Fatalf("metrics = %d", status)
	}

	// Deleting requires typing the username.
	if status := c.json(http.MethodDelete, "/api/tenants/"+itoa(id), map[string]string{"confirm": "wrong"}, nil); status != http.StatusBadRequest {
		t.Fatalf("wrong confirm = %d", status)
	}
	if status := c.json(http.MethodDelete, "/api/tenants/"+itoa(id), map[string]string{"confirm": "alice"}, nil); status != http.StatusOK {
		t.Fatalf("delete = %d", status)
	}
	var trash struct {
		Items []struct {
			Username string `json:"username"`
			DaysLeft int    `json:"days_left"`
		} `json:"items"`
	}
	if status := c.json(http.MethodGet, "/api/trash", nil, &trash); status != http.StatusOK || len(trash.Items) != 1 {
		t.Fatalf("trash = %d %+v", status, trash)
	}
	if trash.Items[0].DaysLeft <= 0 {
		t.Fatalf("days left = %d", trash.Items[0].DaysLeft)
	}
	if status := c.json(http.MethodPost, "/api/trash/"+itoa(id)+"/restore", nil, nil); status != http.StatusOK {
		t.Fatalf("restore = %d", status)
	}
	waitForStatus(t, c, id)
	// Purging requires another trip through the trash.
	if status := c.json(http.MethodDelete, "/api/trash/"+itoa(id), nil, nil); status != http.StatusBadRequest {
		t.Fatalf("purge running tenant = %d, want 400", status)
	}
}

func TestCreateTenantValidation(t *testing.T) {
	c, _ := newTestClient(t)
	cases := []struct {
		body map[string]any
		want int
	}{
		{map[string]any{"username": "root"}, http.StatusBadRequest},
		{map[string]any{"username": "bad name"}, http.StatusBadRequest},
		{map[string]any{"unknown_field": 1}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		if status, _ := c.request(http.MethodPost, "/api/tenants", tc.body); status != tc.want {
			t.Fatalf("%v = %d, want %d", tc.body, status, tc.want)
		}
	}
	createTenant(t, c, "alice")
	if _, body := c.request(http.MethodPost, "/api/tenants", map[string]string{"username": "alice"}); !strings.Contains(string(body), "已存在") {
		t.Fatalf("duplicate create body = %s", body)
	}
}

func TestProtectedTenantCannotBeDeleted(t *testing.T) {
	c, platform := newTestClient(t)
	// Simulate a record for root, which must never be deleted through the API.
	tenant := &domain.Tenant{Username: "root", DisplayName: "root", Port: platform.Cfg.Clawbench.PortStart + 1, Status: domain.StatusRunning, UnitName: "clawbench-root.service"}
	if err := platform.Store.CreateTenant(tenant); err != nil {
		t.Fatal(err)
	}
	if status, body := c.request(http.MethodDelete, "/api/tenants/"+itoa(tenant.ID), map[string]string{"confirm": "root"}); status != http.StatusBadRequest || !strings.Contains(string(body), "root") {
		t.Fatalf("delete root = %d %s", status, body)
	}
}

func TestBackupsOverHTTP(t *testing.T) {
	c, platform := newTestClient(t)
	created := createTenant(t, c, "alice")
	id := tenantID(t, created)
	waitForStatus(t, c, id)
	// The tenant's own database is what gets snapshotted; without it the backup
	// only holds the config and cannot be restored.
	dbPath := filepath.Join(platform.Cfg.TenantDataDir("alice"), "ClawBench.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE kv(k TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	var backup struct {
		Backup map[string]any `json:"backup"`
	}
	if status := c.json(http.MethodPost, "/api/tenants/"+itoa(id)+"/backups", nil, &backup); status != http.StatusCreated {
		t.Fatalf("backup = %d %+v", status, backup)
	}
	backupID, ok := backup.Backup["id"].(float64)
	if !ok {
		t.Fatalf("backup has no id: %+v", backup.Backup)
	}
	backupArg := strconv.FormatInt(int64(backupID), 10)
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if status := c.json(http.MethodGet, "/api/tenants/"+itoa(id)+"/backups", nil, &list); status != http.StatusOK || len(list.Items) != 1 {
		t.Fatalf("list backups = %d %+v", status, list)
	}

	req, _ := http.NewRequest(http.MethodGet, c.srv.URL+"/api/backups/"+backupArg+"/download", http.NoBody)
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 2)
	_, _ = resp.Body.Read(data)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/gzip" {
		t.Fatalf("download = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if data[0] != 0x1f || data[1] != 0x8b {
		t.Fatalf("download is not gzip: %x", data)
	}

	if status := c.json(http.MethodPost, "/api/tenants/"+itoa(id)+"/backups/"+strconv.FormatInt(int64(backupID), 10)+"/restore", nil, nil); status != http.StatusOK {
		t.Fatalf("restore backup = %d", status)
	}
	if status := c.json(http.MethodDelete, "/api/tenants/"+itoa(id)+"/backups/abc", nil, nil); status != http.StatusBadRequest {
		t.Fatalf("bad backup id = %d", status)
	}
	if status := c.json(http.MethodDelete, "/api/tenants/"+itoa(id)+"/backups/"+strconv.FormatInt(int64(backupID), 10), nil, nil); status != http.StatusOK {
		t.Fatalf("delete backup = %d", status)
	}
}

func TestBulkActions(t *testing.T) {
	c, _ := newTestClient(t)
	first := tenantID(t, createTenant(t, c, "alice"))
	second := tenantID(t, createTenant(t, c, "bob"))
	waitForStatus(t, c, first)
	waitForStatus(t, c, second)

	var job struct {
		Job map[string]any `json:"job"`
	}
	if status := c.json(http.MethodPost, "/api/tenants/bulk", map[string]any{"action": "stop", "ids": []int64{first, second}}, &job); status != http.StatusAccepted {
		t.Fatalf("bulk stop = %d", status)
	}
	jobID, ok := job.Job["id"].(float64)
	if !ok {
		t.Fatalf("job has no id: %+v", job.Job)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var got struct {
			Job struct {
				Status string `json:"status"`
				Items  []struct {
					Status string `json:"status"`
				} `json:"items"`
			} `json:"job"`
		}
		if status := c.json(http.MethodGet, "/api/jobs/"+strconv.FormatInt(int64(jobID), 10), nil, &got); status != http.StatusOK {
			t.Fatalf("get job = %d", status)
		}
		if (got.Job.Status == domain.JobSucceeded) && len(got.Job.Items) == 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if status := c.json(http.MethodPost, "/api/tenants/bulk", map[string]any{"action": "explode", "ids": []int64{first}}, nil); status != http.StatusBadRequest {
		t.Fatalf("unknown action = %d", status)
	}
	if status := c.json(http.MethodPost, "/api/tenants/bulk", map[string]any{"action": "stop", "ids": []int64{}}, nil); status != http.StatusBadRequest {
		t.Fatalf("no ids = %d", status)
	}
	if status := c.json(http.MethodGet, "/api/jobs/999", nil, nil); status != http.StatusNotFound {
		t.Fatalf("missing job = %d", status)
	}
}

func TestSettingsAndOverview(t *testing.T) {
	c, _ := newTestClient(t)
	var got struct {
		Settings map[string]any `json:"settings"`
		Mode     string         `json:"mode"`
	}
	if status := c.json(http.MethodGet, "/api/settings", nil, &got); status != http.StatusOK || got.Mode != "dev" {
		t.Fatalf("settings = %d %+v", status, got)
	}
	next := got.Settings
	next["guest_notice"] = "维护中"
	if status := c.json(http.MethodPatch, "/api/settings", next, nil); status != http.StatusOK {
		t.Fatalf("update settings = %d", status)
	}
	next["port_start"] = next["port_end"]
	if status := c.json(http.MethodPatch, "/api/settings", next, nil); status != http.StatusBadRequest {
		t.Fatalf("invalid settings = %d", status)
	}

	createTenant(t, c, "alice")
	var overview struct {
		Total   int              `json:"total"`
		Counts  map[string]int   `json:"counts"`
		Host    map[string]any   `json:"host"`
		Audit   []map[string]any `json:"recent_audit"`
		Mode    string           `json:"mode"`
		Orphans []string         `json:"orphans"`
	}
	if status := c.json(http.MethodGet, "/api/overview", nil, &overview); status != http.StatusOK {
		t.Fatalf("overview = %d", status)
	}
	if overview.Total != 1 || overview.Mode != "dev" {
		t.Fatalf("overview = %+v", overview)
	}
	if len(overview.Audit) == 0 {
		t.Fatal("creating a tenant must be audited")
	}
	var audit struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if status := c.json(http.MethodGet, "/api/audit?action=tenant.create&page=1&page_size=5", nil, &audit); status != http.StatusOK || audit.Total == 0 {
		t.Fatalf("audit = %d %+v", status, audit)
	}
}

func TestScanAndAdopt(t *testing.T) {
	c, _ := newTestClient(t)
	var scan struct {
		Items []map[string]any `json:"items"`
		Mode  string           `json:"mode"`
	}
	if status := c.json(http.MethodGet, "/api/tenants/scan", nil, &scan); status != http.StatusOK || scan.Mode != "dev" {
		t.Fatalf("scan = %d %+v", status, scan)
	}
	if status := c.json(http.MethodPost, "/api/tenants/adopt", map[string]any{"usernames": []string{}}, nil); status != http.StatusBadRequest {
		t.Fatalf("empty adopt = %d", status)
	}
	var started struct {
		Job struct {
			ID int64 `json:"id"`
		} `json:"job"`
	}
	if status := c.json(http.MethodPost, "/api/tenants/adopt", map[string]any{"usernames": []string{"ghost"}}, &started); status != http.StatusAccepted {
		t.Fatalf("adopt = %d", status)
	}
	var job struct {
		Job domain.Job `json:"job"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if status := c.json(http.MethodGet, "/api/jobs/"+strconv.FormatInt(started.Job.ID, 10), nil, &job); status != http.StatusOK {
			t.Fatalf("job = %d", status)
		}
		if job.Job.Status == domain.JobSucceeded || job.Job.Status == domain.JobFailed || job.Job.Status == domain.JobPartial {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("adopt job did not finish: %+v", job.Job)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(job.Job.Items) != 1 || job.Job.Items[0].Error == "" {
		t.Fatalf("unknown user should be reported as an error: %+v", job.Job)
	}
}

func TestCSVImport(t *testing.T) {
	c, _ := newTestClient(t)
	csvBody := []byte("username,display_name,notes\ncarol,Carol,ops\ndave,Dave,\nbad name,Bad,\n")
	var preview struct {
		Rows       []map[string]any `json:"rows"`
		Total      int              `json:"total"`
		Importable int              `json:"importable"`
		Invalid    int              `json:"invalid"`
	}
	if status, body := c.upload("/api/tenants/import/preview", "file", "tenants.csv", csvBody); status != http.StatusOK {
		t.Fatalf("preview = %d %s", status, body)
	} else if err := json.Unmarshal(body, &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Total != 3 || preview.Invalid != 1 || preview.Importable != 2 {
		t.Fatalf("preview = %+v", preview)
	}
	if status, _ := c.upload("/api/tenants/import/preview", "file", "empty.csv", []byte("username\n")); status != http.StatusBadRequest {
		t.Fatalf("header-only csv = %d", status)
	}
	if status, _ := c.upload("/api/tenants/import/preview", "file", "bad.csv", []byte("name\nx\n")); status != http.StatusBadRequest {
		t.Fatalf("missing username column = %d", status)
	}

	var job struct {
		Job map[string]any `json:"job"`
	}
	if status, body := c.upload("/api/tenants/import", "file", "tenants.csv", csvBody); status != http.StatusAccepted {
		t.Fatalf("import = %d %s", status, body)
	} else if err := json.Unmarshal(body, &job); err != nil {
		t.Fatal(err)
	}
	jobID, ok := job.Job["id"].(float64)
	if !ok {
		t.Fatalf("job has no id: %+v", job.Job)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var got struct {
			Job struct {
				Status string `json:"status"`
			} `json:"job"`
		}
		_ = c.json(http.MethodGet, "/api/jobs/"+strconv.FormatInt(int64(jobID), 10), nil, &got)
		if got.Job.Status == domain.JobPartial || got.Job.Status == domain.JobSucceeded {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	req, _ := http.NewRequest(http.MethodGet, c.srv.URL+"/api/jobs/"+strconv.FormatInt(int64(jobID), 10)+"/artifacts/success.csv", http.NoBody)
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(data), "carol") {
		t.Fatalf("success.csv = %d %s", resp.StatusCode, data)
	}
	if status := c.json(http.MethodGet, "/api/jobs/"+strconv.FormatInt(int64(jobID), 10)+"/artifacts/other.csv", nil, nil); status != http.StatusNotFound {
		t.Fatalf("unknown artifact = %d", status)
	}
}

func TestApplyDefaultQuotaEndpoint(t *testing.T) {
	c, _ := newTestClient(t)
	created := createTenant(t, c, "alice")
	id := tenantID(t, created)
	waitForStatus(t, c, id)

	var settings struct {
		Settings map[string]any `json:"settings"`
	}
	if status := c.json(http.MethodGet, "/api/settings", nil, &settings); status != http.StatusOK {
		t.Fatalf("get settings = %d", status)
	}
	next := settings.Settings
	next["default_cpu_quota"] = "300%"
	next["default_memory_max"] = "8G"
	next["default_tasks_max"] = 1024
	if status := c.json(http.MethodPatch, "/api/settings", next, nil); status != http.StatusOK {
		t.Fatalf("update settings = %d", status)
	}

	var started struct {
		Job domain.Job `json:"job"`
	}
	if status := c.json(http.MethodPost, "/api/settings/apply-quota", nil, &started); status != http.StatusAccepted {
		t.Fatalf("apply quota = %d %+v", status, started)
	}
	if started.Job.Total != 1 {
		t.Fatalf("job total = %d, want 1", started.Job.Total)
	}

	deadline := time.Now().Add(20 * time.Second)
	for {
		var got struct {
			Job domain.Job `json:"job"`
		}
		if status := c.json(http.MethodGet, "/api/jobs/"+itoa(started.Job.ID), nil, &got); status != http.StatusOK {
			t.Fatalf("get job = %d", status)
		}
		if got.Job.Status == domain.JobSucceeded || got.Job.Status == domain.JobFailed || got.Job.Status == domain.JobPartial {
			if got.Job.Status != domain.JobSucceeded {
				t.Fatalf("apply job = %+v", got.Job)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("apply job did not finish: %+v", got.Job)
		}
		time.Sleep(50 * time.Millisecond)
	}

	var detail struct {
		Tenant domain.Tenant `json:"tenant"`
	}
	if status := c.json(http.MethodGet, "/api/tenants/"+itoa(id), nil, &detail); status != http.StatusOK {
		t.Fatalf("tenant detail = %d", status)
	}
	if detail.Tenant.CPUQuota != "300%" || detail.Tenant.MemoryMax != "8G" || detail.Tenant.TasksMax != 1024 {
		t.Fatalf("tenant quota = %+v", detail.Tenant)
	}

	// Once every instance matches, there is nothing to apply.
	if status := c.json(http.MethodPost, "/api/settings/apply-quota", nil, nil); status != http.StatusBadRequest {
		t.Fatalf("second apply = %d, want 400", status)
	}
}

func TestStaticFallback(t *testing.T) {
	c, _ := newTestClient(t)
	req, _ := http.NewRequest(http.MethodGet, c.srv.URL+"/unknown/route", http.NoBody)
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	// A built checkout returns the real index.html; a clean checkout (CI does
	// not build the frontend for the test job) returns the "not built yet"
	// placeholder. Both must be a 200 HTML page.
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") || !strings.Contains(string(data), "Clawbench Fleet") {
		t.Fatalf("spa fallback = %d %s %s", resp.StatusCode, resp.Header.Get("Content-Type"), data)
	}
	req, _ = http.NewRequest(http.MethodGet, c.srv.URL+"/api/does-not-exist", http.NoBody)
	resp, err = c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown api = %d", resp.StatusCode)
	}
}
