package ingest_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"example.com/batch-092001-q012/internal/domain"
	"example.com/batch-092001-q012/internal/ingest"
	"example.com/batch-092001-q012/internal/testkit"
)

func validBatch(occurred time.Time) ingest.BatchInput {
	return ingest.BatchInput{
		StandardID:   testkit.StandardID,
		DeviceSerial: testkit.ScaleSerial,
		Items: []json.RawMessage{
			testkit.MakeItem(testkit.ItemOpts{
				Seq: 1, AnimalRef: "TAG-100", Species: "poultry", Trait: "body_weight",
				Value: "1.230", Unit: "kg", OccurredAt: occurred,
			}),
		},
	}
}

func TestAcceptValidReadingAndBindsVersions(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(now)
	w := testkit.New(clock)
	testkit.Seed(t, w)

	res, err := w.Ingest.Ingest(testkit.FarmID, validBatch(now.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Batch.Accepted != 1 || res.Batch.Rejected != 0 {
		t.Fatalf("应全部接受，实际 accepted=%d rejected=%d", res.Batch.Accepted, res.Batch.Rejected)
	}
	r, err := w.Record.View(res.Items[0].ReadingID, testkit.FarmID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Reading.RulesHash == "" || r.Reading.CalibrationID == "" {
		t.Fatal("读数应冻结规则哈希与校准证书引用")
	}
	if r.Reading.AnimalID != testkit.HenA {
		t.Fatalf("机构本地号应解析为 HEN-A，实际 %s", r.Reading.AnimalID)
	}
	if r.EffectiveValue != "1.230" {
		t.Fatalf("生效值应为原始值 1.230，实际 %s", r.EffectiveValue)
	}
}

func TestOfflineBackfillDedupByDeviceSequence(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(now)
	w := testkit.New(clock)
	testkit.Seed(t, w)

	batch := validBatch(now.Add(-2 * time.Hour))
	first, err := w.Ingest.Ingest(testkit.FarmID, batch)
	if err != nil {
		t.Fatal(err)
	}
	// 离线设备整批重传，完全相同的载荷。
	second, err := w.Ingest.Ingest(testkit.FarmID, batch)
	if err != nil {
		t.Fatal(err)
	}
	if second.Items[0].Status != domain.RecordDuplicate {
		t.Fatalf("同设备同序号重传应判 duplicate，实际 %s", second.Items[0].Status)
	}
	if second.Items[0].ReadingID != first.Items[0].ReadingID {
		t.Fatal("幂等重传应指向同一读数")
	}
	if second.Batch.Duplicated != 1 {
		t.Fatalf("批次应统计 1 条重复，实际 %d", second.Batch.Duplicated)
	}

	// 同序号但内容不同 = 可疑冲突，必须拒收而不是覆盖。
	tampered := testkit.MakeItem(testkit.ItemOpts{
		Seq: 1, AnimalRef: "TAG-100", Species: "poultry", Trait: "body_weight",
		Value: "1.990", Unit: "kg", OccurredAt: now.Add(-2 * time.Hour),
	})
	conflict, err := w.Ingest.Ingest(testkit.FarmID, ingest.BatchInput{
		StandardID: testkit.StandardID, DeviceSerial: testkit.ScaleSerial,
		Items: []json.RawMessage{tampered},
	})
	if err != nil {
		t.Fatal(err)
	}
	if conflict.Items[0].Status != domain.RecordRejected {
		t.Fatalf("同序号不同摘要应拒收，实际 %s", conflict.Items[0].Status)
	}
	if !containsReason(conflict.Items[0].Reasons, "digest_mismatch") {
		t.Fatalf("应给出摘要冲突原因，实际 %v", conflict.Items[0].Reasons)
	}
}

func TestValidationRejections(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(now)
	w := testkit.New(clock)
	testkit.Seed(t, w)
	occurred := now.Add(-time.Hour)

	cases := []struct {
		name string
		opts testkit.ItemOpts
		code string
	}{
		{"单位不符", testkit.ItemOpts{Seq: 10, AnimalRef: "TAG-100", Species: "poultry", Trait: "body_weight", Value: "1.2", Unit: "g", OccurredAt: occurred}, "unit_mismatch"},
		{"精度超限", testkit.ItemOpts{Seq: 11, AnimalRef: "TAG-100", Species: "poultry", Trait: "body_weight", Value: "1.2345", Unit: "kg", OccurredAt: occurred}, "precision_exceeded"},
		{"量值超上限", testkit.ItemOpts{Seq: 12, AnimalRef: "TAG-100", Species: "poultry", Trait: "body_weight", Value: "99", Unit: "kg", OccurredAt: occurred}, "value_out_of_range"},
		{"未知个体", testkit.ItemOpts{Seq: 13, AnimalRef: "GHOST", Species: "poultry", Trait: "body_weight", Value: "1.2", Unit: "kg", OccurredAt: occurred}, "unknown_subject"},
		{"设备不适用", testkit.ItemOpts{Seq: 14, AnimalRef: "TAG-100", Species: "poultry", Trait: "backfat_thickness", Value: "12", Unit: "mm", OccurredAt: occurred}, "device_out_of_scope"},
		{"日龄超窗", testkit.ItemOpts{Seq: 15, AnimalRef: "TAG-100", Species: "poultry", Trait: "body_weight", Value: "1.2", Unit: "kg", OccurredAt: now.AddDate(0, 0, 200)}, "measurement_window_missed"},
		{"时钟超前", testkit.ItemOpts{Seq: 16, AnimalRef: "TAG-100", Species: "poultry", Trait: "body_weight", Value: "1.2", Unit: "kg", OccurredAt: now.Add(2 * time.Hour)}, "clock_skew"},
		{"不确定度超标", testkit.ItemOpts{Seq: 17, AnimalRef: "TAG-100", Species: "poultry", Trait: "body_weight", Value: "1.2", Unit: "kg", Uncertainty: "0.5", OccurredAt: occurred}, "uncertainty_exceeded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := w.Ingest.Ingest(testkit.FarmID, ingest.BatchInput{
				StandardID: testkit.StandardID, DeviceSerial: testkit.ScaleSerial,
				Items: []json.RawMessage{testkit.MakeItem(tc.opts)},
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.Items[0].Status != domain.RecordRejected {
				t.Fatalf("应拒收，实际 %s，原因: %v", res.Items[0].Status, res.Items[0].Reasons)
			}
			if !containsReason(res.Items[0].Reasons, tc.code) {
				t.Fatalf("拒收原因应含 %s，实际 %v", tc.code, res.Items[0].Reasons)
			}
			// 拒收记录持久化，属主可在质量收件箱看到。
			inbox, err := w.Quality.OrgInbox(testkit.FarmID, false)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, r := range inbox {
				if r.ID == res.Items[0].RejectionID {
					found = true
				}
			}
			if !found {
				t.Fatal("拒收记录应进入属主质量收件箱")
			}
		})
	}
}

func TestExpiredCalibrationRejectsAndDueWarns(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(now)
	w := testkit.New(clock)
	testkit.Seed(t, w)

	// 种子秤证书：issued -200d、due +30d、expires +100d。
	// 采集时刻晚于 expires -> 拒收。
	res, err := w.Ingest.Ingest(testkit.FarmID, ingest.BatchInput{
		StandardID: testkit.StandardID, DeviceSerial: testkit.ScaleSerial,
		Items: []json.RawMessage{testkit.MakeItem(testkit.ItemOpts{
			Seq: 1, AnimalRef: "TAG-100", Species: "poultry", Trait: "body_weight",
			Value: "1.2", Unit: "kg", OccurredAt: now.AddDate(0, 0, 101),
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsReason(res.Items[0].Reasons, "calibration_invalid") {
		t.Fatalf("校准过期应拒收，原因: %v", res.Items[0].Reasons)
	}

	// 把时钟拨到 due 之后、expires 之前：应接受但挂告警标记。
	due := now.AddDate(0, 0, 31)
	clock.Set(due)
	res2, err := w.Ingest.Ingest(testkit.FarmID, ingest.BatchInput{
		StandardID: testkit.StandardID, DeviceSerial: testkit.ScaleSerial,
		Items: []json.RawMessage{testkit.MakeItem(testkit.ItemOpts{
			Seq: 2, AnimalRef: "TAG-100", Species: "poultry", Trait: "body_weight",
			Value: "1.2", Unit: "kg", OccurredAt: due.Add(-time.Hour),
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Items[0].Status != domain.RecordAccepted {
		t.Fatalf("临期校准应接受，实际 %s", res2.Items[0].Status)
	}
	view, err := w.Record.View(res2.Items[0].ReadingID, testkit.FarmID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Flags) != 1 || view.Flags[0].Code != "calibration_due" {
		t.Fatalf("应挂一条 calibration_due 告警，实际 %+v", view.Flags)
	}
}

func TestDigestMismatchStructuralRejection(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(now)
	w := testkit.New(clock)
	testkit.Seed(t, w)

	good := testkit.MakeItem(testkit.ItemOpts{
		Seq: 1, AnimalRef: "TAG-100", Species: "poultry", Trait: "body_weight",
		Value: "1.2", Unit: "kg", OccurredAt: now.Add(-time.Hour),
	})
	var m map[string]any
	_ = json.Unmarshal(good, &m)
	m["payload_digest"] = "sha256:deadbeef"
	raw, _ := json.Marshal(m)

	res, err := w.Ingest.Ingest(testkit.FarmID, ingest.BatchInput{
		StandardID: testkit.StandardID, DeviceSerial: testkit.ScaleSerial,
		Items: []json.RawMessage{raw},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].Status != domain.RecordRejected ||
		!containsReason(res.Items[0].Reasons, "digest_mismatch") {
		t.Fatalf("伪造摘要应拒收，实际: %v", res.Items[0].Reasons)
	}
}

func containsReason(reasons []string, code string) bool {
	for _, r := range reasons {
		if strings.Contains(r, code) {
			return true
		}
	}
	return false
}
