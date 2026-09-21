package provenance_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"example.com/batch-092001-q012/internal/ingest"
	"example.com/batch-092001-q012/internal/provenance"
	"example.com/batch-092001-q012/internal/records"
	"example.com/batch-092001-q012/internal/testkit"
)

func ingestOne(t *testing.T, w *testkit.World, seq int64, value string, occurred time.Time) string {
	t.Helper()
	res, err := w.Ingest.Ingest(testkit.FarmID, ingest.BatchInput{
		StandardID: testkit.StandardID, DeviceSerial: testkit.ScaleSerial,
		Items: []json.RawMessage{testkit.MakeItem(testkit.ItemOpts{
			Seq: seq, AnimalRef: "TAG-100", Species: "poultry", Trait: "body_weight",
			Value: value, Unit: "kg", OccurredAt: occurred,
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].ReadingID == "" {
		t.Fatalf("记录应被接受: %v", res.Items[0].Reasons)
	}
	return res.Items[0].ReadingID
}

func TestManifestProvesLineageOfDataset(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(now)
	w := testkit.New(clock)
	testkit.Seed(t, w)

	id1 := ingestOne(t, w, 1, "1.10", now.Add(-2*time.Hour))
	id2 := ingestOne(t, w, 2, "1.20", now.Add(-time.Hour))

	manifest, err := w.Prov.Build(provenance.BuildInput{
		CreatorOrg: testkit.FarmID, Description: "本机构遗传分析数据集",
		ReadingIDs: []string{id1, id2, id1}, // 重复编号应去重
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 2 {
		t.Fatalf("重复读数应去重，清单条目数: %d", len(manifest.Entries))
	}
	for _, e := range manifest.Entries {
		if e.RawDigest == "" || e.RulesHash == "" || e.CertDigest == "" || e.CalibrationID == "" {
			t.Fatalf("溯源三元组不完整: %+v", e)
		}
	}

	res, err := w.Prov.Verify(manifest.ID, testkit.FarmID)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Intact || !res.DigestMatches || res.CheckedEntries != 2 {
		t.Fatalf("新生成清单核对应通过，实际 %+v", res)
	}
}

func TestVerifyDetectsRevisionButKeepsRawVerifiable(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(now)
	w := testkit.New(clock)
	testkit.Seed(t, w)

	id := ingestOne(t, w, 1, "1.10", now.Add(-2*time.Hour))
	manifest, err := w.Prov.Build(provenance.BuildInput{
		CreatorOrg: testkit.FarmID, Description: "含修订数据集", ReadingIDs: []string{id},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 后续修订不改原始值；核对时原始三元组仍一致，仅提示生效值变化。
	if _, err := w.Record.AddRevision(records.AddRevisionInput{
		ReadingID: id, NewValue: "1.15", Reason: "设备方复核称重台水平后修订", ByOrgID: testkit.FarmID,
	}); err != nil {
		t.Fatal(err)
	}
	res, err := w.Prov.Verify(manifest.ID, testkit.FarmID)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Intact {
		t.Fatalf("修订不应破坏原始溯源完整性，mismatches=%+v", res.Mismatches)
	}
	found := false
	for _, mm := range res.Mismatches {
		if strings.Contains(mm.Problem, "后续修订") {
			found = true
		}
	}
	if !found {
		t.Fatal("核对应提示生效值因修订而变化，但原始值仍可核对")
	}
}

func TestRawReadingImmutableAndRevisionChain(t *testing.T) {
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(now)
	w := testkit.New(clock)
	testkit.Seed(t, w)

	id := ingestOne(t, w, 1, "1.10", now.Add(-2*time.Hour))
	if _, err := w.Record.AddRevision(records.AddRevisionInput{
		ReadingID: id, NewValue: "1.15", Reason: "复核修订", ByOrgID: testkit.FarmID,
	}); err != nil {
		t.Fatal(err)
	}
	view, err := w.Record.View(id, testkit.FarmID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Reading.ValueText != "1.10" {
		t.Fatalf("原始值不可变，应仍是 1.10，实际 %s", view.Reading.ValueText)
	}
	if view.EffectiveValue != "1.15" {
		t.Fatalf("生效值应为最新修订 1.15，实际 %s", view.EffectiveValue)
	}
	if len(view.Revisions) != 1 || view.Revisions[0].Supersedes != "" {
		t.Fatal("首条修订的 supersedes 应为空")
	}
	if _, err := w.Record.AddRevision(records.AddRevisionInput{
		ReadingID: id, NewValue: "1.12", Reason: "二次修订", ByOrgID: testkit.FarmID,
	}); err != nil {
		t.Fatal(err)
	}
	view, _ = w.Record.View(id, testkit.FarmID)
	if len(view.Revisions) != 2 || view.Revisions[1].Supersedes != view.Revisions[0].ID {
		t.Fatal("第二条修订应指向第一条，形成修订链")
	}
}
