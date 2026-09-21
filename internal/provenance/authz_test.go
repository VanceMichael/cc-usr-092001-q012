package provenance_test

import (
	"encoding/json"
	"testing"
	"time"

	"example.com/batch-092001-q012/internal/apperr"
	"example.com/batch-092001-q012/internal/ingest"
	"example.com/batch-092001-q012/internal/provenance"
	"example.com/batch-092001-q012/internal/records"
	"example.com/batch-092001-q012/internal/testkit"
)

// 手工清单只能包含本机构读数；跨机构数据集必须走经批准的切片。
func TestManualManifestRejectsCrossOrgReadings(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(now)
	w := testkit.New(clock)
	testkit.Seed(t, w)

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
	id := res.Items[0].ReadingID

	_, err = w.Prov.Build(provenance.BuildInput{
		CreatorOrg: testkit.InstituteID, Description: "越权数据集", ReadingIDs: []string{id},
	})
	if ae, ok := err.(*apperr.Error); !ok || ae.Code != apperr.Forbidden {
		t.Fatalf("跨机构手工建清单应被禁止，实际: %v", err)
	}

	// 非属主机构既不能查看读数视图，也不能修订他人读数。
	if _, err := w.Record.View(id, testkit.InstituteID); err == nil {
		t.Fatal("跨机构查看原始读数视图应被拒绝")
	}
	_, err = w.Record.AddRevision(records.AddRevisionInput{
		ReadingID: id, NewValue: "1.3", Reason: "越权修订", ByOrgID: testkit.InstituteID,
	})
	if ae, ok := err.(*apperr.Error); !ok || ae.Code != apperr.Forbidden {
		t.Fatalf("跨机构修订应被禁止（应走质量标记），实际: %v", err)
	}

	// 手工清单不得冒用切片申请编号绕过属主校验。
	_, err = w.Prov.Build(provenance.BuildInput{
		CreatorOrg: testkit.InstituteID, Description: "冒用",
		ReadingIDs: []string{id}, SliceReqID: "SLICE-FAKE",
	})
	if ae, ok := err.(*apperr.Error); !ok || ae.Code != apperr.Validation {
		t.Fatalf("手工清单携带切片编号应被校验拒绝，实际: %v", err)
	}
}
