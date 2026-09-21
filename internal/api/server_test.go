package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"example.com/batch-092001-q012/internal/api"
	"example.com/batch-092001-q012/internal/canon"
)

const token = "consortium-secret"

type client struct {
	t      *testing.T
	server *api.Server
}

func (c *client) do(method, path, org, bearer string, body any) (int, map[string]any) {
	c.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Org-ID", org)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	c.server.Handler().ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

func TestEndToEndGatewayFlow(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	server := api.NewServer(token, func() time.Time { return now })
	c := &client{t: t, server: server}

	// 1) 联合体登记三类机构。
	for _, o := range []map[string]string{
		{"org_id": "FARM1", "name": "种禽场", "role": "farm"},
		{"org_id": "FARM2", "name": "猪场", "role": "farm"},
		{"org_id": "INST1", "name": "畜牧研究院", "role": "institute"},
	} {
		if code, _ := c.do("POST", "/v1/orgs", "CONSORTIUM", token, o); code != http.StatusCreated {
			t.Fatalf("登记机构 %s 返回 %d", o["org_id"], code)
		}
	}
	// 无管理令牌不能登记机构。
	if code, body := c.do("POST", "/v1/orgs", "CONSORTIUM", "wrong", map[string]string{"org_id": "X", "name": "x", "role": "farm"}); code != http.StatusUnauthorized {
		t.Fatalf("错误管理令牌应 401，实际 %d %v", code, body)
	}
	// 无机构头不能访问业务接口。
	if code, _ := c.do("GET", "/v1/devices/SCALE-1", "", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("缺少 X-Org-ID 应 401，实际 %d", code)
	}

	// 2) 联合体发布标准。
	rules := map[string]any{
		"species": []string{"poultry"},
		"traits": []map[string]any{{
			"code": "body_weight", "species": "poultry", "name": "体重", "unit": "kg",
			"decimals": 3, "min_value": "0.01", "max_value": "20",
			"min_age_days": 1, "max_age_days": 120,
		}},
		"allowed_device_kinds":   []string{"platform_scale"},
		"max_clock_skew_minutes": 60,
		"max_batch_lag_hours":    720,
	}
	if code, body := c.do("POST", "/v1/standards/STD-L/versions", "CONSORTIUM", token, map[string]any{
		"version": "2026.1", "effective_at": now.AddDate(-1, 0, 0).Format(time.RFC3339), "rules": rules,
	}); code != http.StatusCreated {
		t.Fatalf("发布标准返回 %d: %v", code, body)
	}

	// 3) 养殖场登记设备与校准。
	if code, body := c.do("POST", "/v1/devices", "FARM1", "", map[string]any{
		"serial": "SCALE-1", "kind": "platform_scale", "vendor_model": "ACME",
		"species_scope": []string{"poultry"}, "trait_scope": []string{"body_weight"},
	}); code != http.StatusCreated {
		t.Fatalf("登记设备返回 %d: %v", code, body)
	}
	if code, body := c.do("POST", "/v1/devices/SCALE-1/calibrations", "FARM1", "", map[string]any{
		"issued_at":  now.AddDate(0, 0, -30).Format(time.RFC3339),
		"due_at":     now.AddDate(0, 0, 30).Format(time.RFC3339),
		"expires_at": now.AddDate(0, 0, 100).Format(time.RFC3339),
		"cert_ref":   "CERT-1", "cert_digest": "sha256:abc", "issued_by": "计量院",
		"max_uncertainty": map[string]string{"body_weight": "0.01"},
	}); code != http.StatusCreated {
		t.Fatalf("登记校准返回 %d: %v", code, body)
	}

	// 4) 登记个体与别名。
	dob := now.AddDate(0, 0, -60)
	if code, body := c.do("POST", "/v1/subjects", "FARM1", "", map[string]any{
		"animal_id": "HEN-A", "species": "poultry", "dob": dob.Format(time.RFC3339),
		"generation": 2, "sex": "F",
	}); code != http.StatusCreated {
		t.Fatalf("登记个体返回 %d: %v", code, body)
	}
	if code, body := c.do("PUT", "/v1/orgs/FARM1/aliases/TAG-100", "FARM1", "", map[string]string{
		"canonical_id": "HEN-A",
	}); code != http.StatusOK {
		t.Fatalf("绑定别名返回 %d: %v", code, body)
	}

	// 5) 上传采集记录（带正确摘要）。
	occurred := now.Add(-time.Hour)
	item := map[string]any{
		"source_sequence": 1, "animal_ref": "TAG-100", "species": "poultry",
		"trait_code": "body_weight", "value": "1.230", "unit": "kg",
		"occurred_at": occurred.Format(time.RFC3339),
	}
	digest, err := canon.Hash(item)
	if err != nil {
		t.Fatal(err)
	}
	item["payload_digest"] = digest
	raw, _ := json.Marshal(item)
	var single any = json.RawMessage(raw)
	_ = single
	if code, body := c.do("POST", "/v1/ingest/batches", "FARM1", "", map[string]any{
		"standard_id": "STD-L", "device_serial": "SCALE-1",
		"items": []json.RawMessage{raw},
	}); code != http.StatusOK {
		t.Fatalf("上传批次返回 %d: %v", code, body)
	} else {
		batch := body["batch"].(map[string]any)
		if batch["accepted"].(float64) != 1 {
			t.Fatalf("应接受 1 条，实际 %v", batch)
		}
	}

	// 6) 同批次幂等重传：duplicated=1。
	if code, body := c.do("POST", "/v1/ingest/batches", "FARM1", "", map[string]any{
		"standard_id": "STD-L", "device_serial": "SCALE-1",
		"items": []json.RawMessage{raw},
	}); code != http.StatusOK || body["batch"].(map[string]any)["duplicated"].(float64) != 1 {
		t.Fatalf("重传应计 1 条重复，code=%d body=%v", code, body)
	}

	// 7) 非法记录（单位错误）应拒收并进质量收件箱。
	bad := map[string]any{
		"source_sequence": 2, "animal_ref": "HEN-A", "species": "poultry",
		"trait_code": "body_weight", "value": "1.230", "unit": "g",
		"occurred_at": occurred.Format(time.RFC3339),
	}
	badDigest, _ := canon.Hash(bad)
	bad["payload_digest"] = badDigest
	badRaw, _ := json.Marshal(bad)
	if code, body := c.do("POST", "/v1/ingest/batches", "FARM1", "", map[string]any{
		"standard_id": "STD-L", "device_serial": "SCALE-1",
		"items": []json.RawMessage{badRaw},
	}); code != http.StatusOK || body["batch"].(map[string]any)["rejected"].(float64) != 1 {
		t.Fatalf("非法记录应拒收，code=%d body=%v", code, body)
	}
	if code, body := c.do("GET", "/v1/quality/rejections", "FARM1", "", nil); code != http.StatusOK {
		t.Fatalf("质量收件箱返回 %d", code)
	} else {
		list := body["rejections"].([]any)
		if len(list) != 1 {
			t.Fatalf("收件箱应有 1 条拒收，实际 %d", len(list))
		}
	}

	// 8) 研究机构申请切片 → 联合体审批 → 假名化导出并自动生成清单。
	sliceBody := map[string]any{
		"purpose": "体重遗传力研究", "species": []string{"poultry"},
		"trait_codes":     []string{"body_weight"},
		"date_from":       now.Add(-30 * 24 * time.Hour).Format(time.RFC3339),
		"date_to":         now.Add(time.Hour).Format(time.RFC3339),
		"include_lineage": true,
	}
	code, body := c.do("POST", "/v1/slices", "INST1", "", sliceBody)
	if code != http.StatusCreated {
		t.Fatalf("切片申请返回 %d: %v", code, body)
	}
	sliceID := body["slice_request_id"].(string)
	if code, body := c.do("POST", "/v1/slices/"+sliceID+"/review", "CONSORTIUM", token, map[string]any{
		"approve": true, "note": "同意",
	}); code != http.StatusOK {
		t.Fatalf("审批返回 %d: %v", code, body)
	}
	code, body = c.do("GET", "/v1/slices/"+sliceID+"/export", "INST1", "", nil)
	if code != http.StatusOK {
		t.Fatalf("导出返回 %d: %v", code, body)
	}
	recordsList := body["records"].([]any)
	if len(recordsList) != 1 {
		t.Fatalf("切片应含 1 条记录，实际 %d", len(recordsList))
	}
	rec := recordsList[0].(map[string]any)
	if rec["pseudonym"] == "HEN-A" || rec["pseudonym"] == "TAG-100" {
		t.Fatal("导出记录必须假名化")
	}
	manifestID, ok := body["manifest_id"].(string)
	if !ok || manifestID == "" {
		t.Fatal("导出应自动生成溯源清单编号")
	}

	// 9) 溯源清单核对通过，并能读到原始采集、规则与校准三元组。
	if code, body := c.do("POST", "/v1/manifests/"+manifestID+"/verify", "INST1", "", nil); code != http.StatusOK {
		t.Fatalf("清单核对返回 %d: %v", code, body)
	} else if body["intact"] != true {
		t.Fatalf("清单应完整，实际 %v", body)
	}
	if code, body := c.do("GET", "/v1/manifests/"+manifestID, "INST1", "", nil); code != http.StatusOK {
		t.Fatalf("读取清单返回 %d", code)
	} else {
		entries := body["entries"].([]any)
		e := entries[0].(map[string]any)
		if e["raw_payload_digest"] == "" || e["rules_hash"] == "" || e["cert_digest"] == "" {
			t.Fatalf("溯源三元组不完整: %v", e)
		}
	}
}

func TestHealth(t *testing.T) {
	server := api.NewServer(token, nil)
	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("健康检查应 200，实际 %d", rec.Code)
	}
}
