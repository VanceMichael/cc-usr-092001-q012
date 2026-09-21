package ingest_test

import (
	"encoding/json"
	"testing"
	"time"

	"example.com/batch-092001-q012/internal/apperr"
	"example.com/batch-092001-q012/internal/ingest"
	"example.com/batch-092001-q012/internal/testkit"
)

// 非设备归属机构不能拿他方设备序列号上传——信封级拒绝，不产生任何记录。
func TestNonOwnerOrgCannotUploadWithOthersDevice(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(now)
	w := testkit.New(clock)
	testkit.Seed(t, w)

	item := testkit.MakeItem(testkit.ItemOpts{
		Seq: 1, AnimalRef: testkit.Pig1, Species: "swine", Trait: "backfat_thickness",
		Value: "12.0", Unit: "mm", OccurredAt: now.Add(-time.Hour),
	})
	_, err := w.Ingest.Ingest(testkit.FarmID, ingest.BatchInput{
		StandardID: testkit.StandardID, DeviceSerial: testkit.UltraSerial, // 属于 FARM2
		Items: []json.RawMessage{item},
	})
	if ae, ok := err.(*apperr.Error); !ok || ae.Code != apperr.Forbidden {
		t.Fatalf("非属主机构用他方设备上传应被禁止，实际: %v", err)
	}
}
