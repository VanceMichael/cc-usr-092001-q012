package records_test

import (
	"encoding/json"
	"testing"
	"time"

	"example.com/batch-092001-q012/internal/apperr"
	"example.com/batch-092001-q012/internal/ingest"
	"example.com/batch-092001-q012/internal/records"
	"example.com/batch-092001-q012/internal/testkit"
)

func ingestReading(t *testing.T, w *testkit.World) string {
	t.Helper()
	now := w.Clock.Now()
	res, err := w.Ingest.Ingest(testkit.FarmID, ingest.BatchInput{
		StandardID: testkit.StandardID, DeviceSerial: testkit.ScaleSerial,
		Items: []json.RawMessage{testkit.MakeItem(testkit.ItemOpts{
			Seq: 1, AnimalRef: "TAG-100", Species: "poultry", Trait: "body_weight",
			Value: "1.2", Unit: "kg", OccurredAt: now.Add(-time.Hour),
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].ReadingID == "" {
		t.Fatalf("记录应接受: %v", res.Items[0].Reasons)
	}
	return res.Items[0].ReadingID
}

// 跨机构质量标记是允许的反馈通道，但无关第三方不能处理它。
func TestCrossOrgFlagResolutionAuthorization(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(now)
	w := testkit.New(clock)
	testkit.Seed(t, w)
	id := ingestReading(t, w)

	// 另一养殖场对 FARM1 的数据挂质量标记（跨机构反馈，允许）。
	flag, err := w.Record.AddFlag(records.AddFlagInput{
		ReadingID: id, Severity: "warning", Code: "suspect_outlier",
		Detail: "同日同批次该值明显偏离", ByOrgID: testkit.OtherFarm,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 无关第三方（研究院）不能处理该标记。
	_, err = w.Record.ResolveFlag(flag.ID, testkit.InstituteID, "已核查", false)
	if ae, ok := err.(*apperr.Error); !ok || ae.Code != apperr.Forbidden {
		t.Fatalf("无关机构处理标记应被禁止，实际: %v", err)
	}
	// 数据属主可以处理。
	got, err := w.Record.ResolveFlag(flag.ID, testkit.FarmID, "复测确认异常，已隔离", false)
	if err != nil {
		t.Fatalf("数据属主应可处理标记: %v", err)
	}
	if got.Status != "resolved" {
		t.Fatalf("标记应为 resolved，实际 %s", got.Status)
	}
}

// 重复处理同一标记应冲突。
func TestFlagCannotResolveTwice(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(now)
	w := testkit.New(clock)
	testkit.Seed(t, w)
	id := ingestReading(t, w)

	flag, err := w.Record.AddFlag(records.AddFlagInput{
		ReadingID: id, Severity: "info", Code: "note", Detail: "备注", ByOrgID: testkit.FarmID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Record.ResolveFlag(flag.ID, testkit.FarmID, "处理", false); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Record.ResolveFlag(flag.ID, testkit.FarmID, "再处理", false); err == nil {
		t.Fatal("已闭环标记不能重复处理")
	}
}
