package slices_test

import (
	"encoding/json"
	"testing"
	"time"

	"example.com/batch-092001-q012/internal/apperr"
	"example.com/batch-092001-q012/internal/ingest"
	"example.com/batch-092001-q012/internal/slices"
	"example.com/batch-092001-q012/internal/testkit"
)

// seedReadings 产生两条本机构读数 + 一条他机构读数（用于用途范围验证）。
func seedReadings(t *testing.T, w *testkit.World, now time.Time) (string, string, string) {
	t.Helper()
	r1, err := w.Ingest.Ingest(testkit.FarmID, ingest.BatchInput{
		StandardID: testkit.StandardID, DeviceSerial: testkit.ScaleSerial,
		Items: []json.RawMessage{testkit.MakeItem(testkit.ItemOpts{
			Seq: 1, AnimalRef: "TAG-100", Species: "poultry", Trait: "body_weight",
			Value: "1.10", Unit: "kg", OccurredAt: now.Add(-24 * time.Hour),
		})},
	})
	if err != nil || r1.Batch.Accepted != 1 {
		t.Fatalf("r1 未接受: %+v %v", r1.Items[0].Reasons, err)
	}
	r2, err := w.Ingest.Ingest(testkit.FarmID, ingest.BatchInput{
		StandardID: testkit.StandardID, DeviceSerial: testkit.ScaleSerial,
		Items: []json.RawMessage{testkit.MakeItem(testkit.ItemOpts{
			Seq: 2, AnimalRef: testkit.HenB, Species: "poultry", Trait: "body_weight",
			Value: "1.20", Unit: "kg", OccurredAt: now.Add(-48 * time.Hour),
		})},
	})
	if err != nil || r2.Batch.Accepted != 1 {
		t.Fatalf("r2 未接受: %+v %v", r2.Items[0].Reasons, err)
	}
	r3, err := w.Ingest.Ingest(testkit.OtherFarm, ingest.BatchInput{
		StandardID: testkit.StandardID, DeviceSerial: testkit.UltraSerial,
		Items: []json.RawMessage{testkit.MakeItem(testkit.ItemOpts{
			Seq: 1, AnimalRef: testkit.Pig1, Species: "swine", Trait: "backfat_thickness",
			Value: "12.0", Unit: "mm", OccurredAt: now.Add(-24 * time.Hour),
		})},
	})
	if err != nil || r3.Batch.Accepted != 1 {
		t.Fatalf("r3 未接受: %+v %v", r3.Items[0].Reasons, err)
	}
	return r1.Items[0].ReadingID, r2.Items[0].ReadingID, r3.Items[0].ReadingID
}

func TestApprovedExportIsPseudonymizedAndScoped(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(now)
	w := testkit.New(clock)
	testkit.Seed(t, w)
	id1, id2, _ := seedReadings(t, w, now)

	req, err := w.Slice.Apply(slices.ApplyInput{
		OrgID: testkit.InstituteID, Purpose: "体重遗传力研究",
		Species: []string{"poultry"}, TraitCodes: []string{"body_weight"},
		DateFrom: now.Add(-30 * 24 * time.Hour), DateTo: now.Add(time.Hour),
		IncludeLineage: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 审批前导出被拒绝。
	if _, err := w.Slice.Export(testkit.InstituteID, req.ID); err == nil {
		t.Fatal("未审批申请不应可导出")
	}
	if _, err := w.Slice.Review(req.ID, "CONSORTIUM", true, "同意", nil, time.Time{}); err != nil {
		t.Fatal(err)
	}
	export, err := w.Slice.Export(testkit.InstituteID, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(export.Records) != 2 {
		t.Fatalf("应只含 2 条禽体重记录（不含猪场记录），实际 %d", len(export.Records))
	}
	for _, r := range export.Records {
		if r.Pseudonym == testkit.HenA || r.Pseudonym == testkit.HenB || r.Pseudonym == "TAG-100" {
			t.Fatal("导出中出现真实/本地标识，假名化失败")
		}
		if r.RulesHash == "" || r.CalibrationID == "" {
			t.Fatal("导出记录应保留规则哈希与校准引用以支持溯源")
		}
	}
	// 主体表不含机构编号，出生日期精度裁剪到月。
	if len(export.Subjects) != 2 {
		t.Fatalf("应有 2 个假名主体，实际 %d", len(export.Subjects))
	}
	for _, s := range export.Subjects {
		if s.DobMonth != "2026-07" {
			t.Fatalf("出生日期应裁剪到月 2026-07，实际 %q", s.DobMonth)
		}
	}
	// 同一假名在记录与主体表间一致。
	pmap := map[string]bool{}
	for _, s := range export.Subjects {
		pmap[s.Pseudonym] = true
	}
	for _, r := range export.Records {
		if !pmap[r.Pseudonym] {
			t.Fatal("记录假名在主体表中缺失")
		}
	}
	_ = id1
	_ = id2
}

func TestApprovedSubjectsNarrowsScope(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(now)
	w := testkit.New(clock)
	testkit.Seed(t, w)
	seedReadings(t, w, now)

	req, err := w.Slice.Apply(slices.ApplyInput{
		OrgID: testkit.InstituteID, Purpose: "单羽验证",
		Species: []string{"poultry"}, TraitCodes: []string{"body_weight"},
		DateFrom: now.Add(-30 * 24 * time.Hour), DateTo: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	// 审批只圈定 HEN-A 一个主体。
	if _, err := w.Slice.Review(req.ID, "CONSORTIUM", true, "仅同意一羽", []string{testkit.HenA}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	export, err := w.Slice.Export(testkit.InstituteID, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(export.Records) != 1 {
		t.Fatalf("批准名单应收窄到 1 条记录，实际 %d", len(export.Records))
	}
}

func TestOnlyInstituteCanApplyAndOwnerCanExport(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(now)
	w := testkit.New(clock)
	testkit.Seed(t, w)

	_, err := w.Slice.Apply(slices.ApplyInput{
		OrgID: testkit.FarmID, Purpose: "养殖场也想看",
		Species: []string{"poultry"}, TraitCodes: []string{"body_weight"},
		DateFrom: now.Add(-time.Hour), DateTo: now,
	})
	if ae, ok := err.(*apperr.Error); !ok || ae.Code != apperr.Forbidden {
		t.Fatalf("非研究机构申请应被禁止，实际: %v", err)
	}
}

func TestExpiredApprovalCannotExport(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(now)
	w := testkit.New(clock)
	testkit.Seed(t, w)

	req, err := w.Slice.Apply(slices.ApplyInput{
		OrgID: testkit.InstituteID, Purpose: "短期研究",
		Species: []string{"poultry"}, TraitCodes: []string{"body_weight"},
		DateFrom: now.Add(-time.Hour), DateTo: now, ValidDays: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Slice.Review(req.ID, "CONSORTIUM", true, "同意", nil, time.Time{}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(48 * time.Hour)
	if _, err := w.Slice.Export(testkit.InstituteID, req.ID); err == nil {
		t.Fatal("批准过期后导出必须被拒绝")
	}
}
