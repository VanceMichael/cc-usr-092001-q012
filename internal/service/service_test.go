package service

import (
	"testing"
	"time"

	"example.com/batch-092001-q012/internal/domain"
	"example.com/batch-092001-q012/internal/store"
)

// 固定“当前时间”，保证测试可重复。
var testNow = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

func newTestService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	return New(st, func() time.Time { return testNow })
}

// fixture 登记一套基础事实：标准 v1（生效于 2026-01-01）、物种 PIG、
// 性状 BACKFAT（mm，1 位小数，5~50）、日龄窗口 150~200、格式 FMT-1、
// 设备 DEV-1（机构 ORG-A）与覆盖 2026 全年的校准证书、个体 S1。
type fixture struct {
	svc      *Service
	standard domain.StandardVersion
}

func setupFixture(t *testing.T) fixture {
	t.Helper()
	svc := newTestService(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("准备基础事实失败: %v", err)
		}
	}
	standard, err := svc.RegisterStandard("Q012", "1.0", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	must(err)
	_, err = svc.RegisterSpecies(standard.ID, "PIG", "生猪")
	must(err)
	minV, maxV := 5.0, 50.0
	_, err = svc.RegisterTrait(standard.ID, "PIG", "BACKFAT", "背膘厚", "mm", 1, &minV, &maxV)
	must(err)
	_, err = svc.RegisterProtocol(standard.ID, "PIG", "BACKFAT", 150, 200)
	must(err)
	_, err = svc.RegisterFormat(standard.ID, "FMT-1", "1", "基础格式")
	must(err)
	_, err = svc.ActivateStandard(standard.ID)
	must(err)
	_, err = svc.RegisterDevice("DEV-1", "超声仪A型", "ORG-A", []string{"PIG"}, []string{"BACKFAT"})
	must(err)
	_, err = svc.RegisterCalibration("DEV-1", "CAL-2026-001", "计量院",
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC), "sha256:cert")
	must(err)
	_, err = svc.RegisterSubject("S1", "PIG", "ORG-A", "2026-01-10", "", "", 3)
	must(err)
	return fixture{svc: svc, standard: standard}
}

func validItem() UploadItem {
	// 2026-07-01 距 2026-01-10 为 172 天，落在 150~200 窗口内
	return UploadItem{
		SubjectRef:     "S1",
		TraitCode:      "BACKFAT",
		Value:          "12.3",
		Unit:           "mm",
		OccurredAt:     "2026-07-01T08:30:00+08:00",
		SourceSequence: 1,
	}
}

func uploadOne(t *testing.T, svc *Service, item UploadItem) ItemResult {
	t.Helper()
	result, err := svc.IngestBatch("ORG-A", "DEV-1", "FMT-1", []UploadItem{item})
	if err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("期望 1 条结果，实际 %d", len(result.Items))
	}
	return result.Items[0]
}

func TestIngestAcceptsValidItem(t *testing.T) {
	f := setupFixture(t)
	item := uploadOne(t, f.svc, validItem())
	if item.Status != "accepted" {
		t.Fatalf("期望接受，实际 %s，原因 %v", item.Status, item.RejectReasons)
	}
	observation, revisions, marks, err := f.svc.GetObservation(item.ObservationID)
	if err != nil {
		t.Fatal(err)
	}
	if observation.StandardID != f.standard.ID {
		t.Fatalf("观测未绑定接收时的标准版本")
	}
	if observation.CalibrationID == "" {
		t.Fatalf("观测未绑定校准证书")
	}
	if CurrentValue(observation, revisions) != "12.3" || len(marks) != 0 {
		t.Fatalf("当前值或质量标记不符合预期")
	}
}

func TestIngestRejectsUnitPrecisionRangeAge(t *testing.T) {
	f := setupFixture(t)
	cases := []struct {
		name   string
		mutate func(*UploadItem)
		reason string
	}{
		{"单位不符", func(i *UploadItem) { i.Unit = "cm" }, "UNIT_MISMATCH"},
		{"精度超限", func(i *UploadItem) { i.Value = "12.34" }, "PRECISION_EXCEEDED"},
		{"超出取值范围", func(i *UploadItem) { i.Value = "99.9" }, "VALUE_OUT_OF_RANGE"},
		{"日龄在窗口外", func(i *UploadItem) { i.OccurredAt = "2026-03-01T08:30:00+08:00" }, "AGE_OUT_OF_WINDOW"},
		{"未知个体", func(i *UploadItem) { i.SubjectRef = "S-X" }, "UNKNOWN_SUBJECT"},
		{"未知性状", func(i *UploadItem) { i.TraitCode = "HEIGHT" }, "UNKNOWN_TRAIT"},
		{"校准未覆盖", func(i *UploadItem) { i.OccurredAt = "2027-06-01T08:30:00+08:00" }, "NO_VALID_CALIBRATION"},
		{"非法数值", func(i *UploadItem) { i.Value = "abc" }, "INVALID_VALUE"},
	}
	for n, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := validItem()
			item.SourceSequence = int64(100 + n)
			tc.mutate(&item)
			result := uploadOne(t, f.svc, item)
			if result.Status != "rejected" {
				t.Fatalf("期望拒收，实际 %s", result.Status)
			}
			found := false
			for _, reason := range result.RejectReasons {
				if reason == tc.reason {
					found = true
				}
			}
			if !found {
				t.Fatalf("拒收原因 %v 中缺少 %s", result.RejectReasons, tc.reason)
			}
		})
	}
}

func TestIngestRejectsDeviceScopeAndOrg(t *testing.T) {
	f := setupFixture(t)
	// 设备适用范围不含该性状
	if _, err := f.svc.RegisterDevice("DEV-2", "地秤", "ORG-A", []string{"PIG"}, []string{"WEIGHT"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RegisterCalibration("DEV-2", "CAL-2026-002", "计量院",
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), "sha256:cert2"); err != nil {
		t.Fatal(err)
	}
	result, err := f.svc.IngestBatch("ORG-A", "DEV-2", "FMT-1", []UploadItem{validItem()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Items[0].Status != "rejected" {
		t.Fatalf("设备适用范围不符应被拒收")
	}
	// 设备归属机构不符
	if _, err := f.svc.IngestBatch("ORG-B", "DEV-1", "FMT-1", []UploadItem{validItem()}); !IsCode(err, "DEVICE_ORG_MISMATCH") {
		t.Fatalf("期望 DEVICE_ORG_MISMATCH，实际 %v", err)
	}
	// 未登记设备
	if _, err := f.svc.IngestBatch("ORG-A", "DEV-X", "FMT-1", []UploadItem{validItem()}); !IsCode(err, "UNKNOWN_DEVICE") {
		t.Fatalf("期望 UNKNOWN_DEVICE，实际 %v", err)
	}
}

func TestOfflineResendDeduplicatesByDeviceSequence(t *testing.T) {
	f := setupFixture(t)
	first := uploadOne(t, f.svc, validItem())
	if first.Status != "accepted" {
		t.Fatalf("首次上传应被接受")
	}
	// 离线设备补传同一批数据：同设备序号 + 同来源序号应去重
	second := uploadOne(t, f.svc, validItem())
	if second.Status != "duplicate" || second.DuplicateOf != first.ObservationID {
		t.Fatalf("补传应去重，实际 %+v", second)
	}
	// 被拒记录不占用序号：修正后可用同一来源序号重新上报
	bad := validItem()
	bad.SourceSequence = 7
	bad.Unit = "cm"
	if r := uploadOne(t, f.svc, bad); r.Status != "rejected" {
		t.Fatalf("错误单位应被拒收")
	}
	good := validItem()
	good.SourceSequence = 7
	if r := uploadOne(t, f.svc, good); r.Status != "accepted" {
		t.Fatalf("修正后重报应被接受，实际 %s %v", r.Status, r.RejectReasons)
	}
}

func TestStandardUpgradeDoesNotReinterpretOldData(t *testing.T) {
	f := setupFixture(t)
	// 升级标准 v2：单位改为 cm、精度 2 位，2026-08-01 起生效
	v2, err := f.svc.RegisterStandard("Q012", "2.0", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RegisterSpecies(v2.ID, "PIG", "生猪"); err != nil {
		t.Fatal(err)
	}
	minV, maxV := 0.5, 5.0
	if _, err := f.svc.RegisterTrait(v2.ID, "PIG", "BACKFAT", "背膘厚", "cm", 2, &minV, &maxV); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RegisterProtocol(v2.ID, "PIG", "BACKFAT", 150, 250); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RegisterFormat(v2.ID, "FMT-1", "1", "基础格式"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ActivateStandard(v2.ID); err != nil {
		t.Fatal(err)
	}
	// 生效后旧版本规则冻结
	if _, err := f.svc.RegisterTrait(f.standard.ID, "PIG", "HEIGHT", "体高", "cm", 0, nil, nil); !IsCode(err, "CONFLICT") {
		t.Fatalf("已生效标准不应再允许登记规则，实际 %v", err)
	}
	// 升级前的采集仍按 v1（mm）校验，不重释
	oldItem := validItem()
	oldItem.OccurredAt = "2026-07-15T08:30:00+08:00"
	oldItem.SourceSequence = 11
	r1 := uploadOne(t, f.svc, oldItem)
	if r1.Status != "accepted" {
		t.Fatalf("升级前的 mm 数据应按 v1 接受，实际 %v", r1.RejectReasons)
	}
	obs, _, _, _ := f.svc.GetObservation(r1.ObservationID)
	if obs.StandardID != f.standard.ID {
		t.Fatalf("旧数据应保留 v1 标准版本绑定")
	}
	// 升级后的采集按 v2（cm）校验：mm 单位被拒
	newItem := validItem()
	newItem.OccurredAt = "2026-08-10T08:30:00+08:00"
	newItem.SourceSequence = 12
	r2 := uploadOne(t, f.svc, newItem)
	if r2.Status != "rejected" {
		t.Fatalf("升级后 mm 单位应被拒收")
	}
	newItem.Unit = "cm"
	newItem.Value = "1.23"
	r3 := uploadOne(t, f.svc, newItem)
	if r3.Status != "accepted" {
		t.Fatalf("升级后 cm 数据应按 v2 接受，实际 %v", r3.RejectReasons)
	}
}

func TestMergeKeepsPedigreeChain(t *testing.T) {
	f := setupFixture(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// S2 是同一个体的正确编号，S1 为误发编号；C1 是 S1 的后代
	must(error2(f.svc.RegisterSubject("S2", "PIG", "ORG-A", "2026-01-10", "", "", 3)))
	must(error2(f.svc.RegisterSubject("C1", "PIG", "ORG-A", "2026-08-01", "S1", "", 4)))
	_, err := f.svc.MergeSubjects("S1", "S2", "标识纠错")
	must(err)
	// 谱系沿合并链解析：C1 的父代应解析为规范编号 S2
	node, err := f.svc.Pedigree("C1", 1)
	must(err)
	if node.Sire == nil || node.Sire.Ref != "S2" {
		t.Fatalf("合并后亲缘链中断: %+v", node.Sire)
	}
	// 旧标识上报的观测解析到规范个体
	item := validItem()
	item.SubjectRef = "S1"
	item.SourceSequence = 21
	r := uploadOne(t, f.svc, item)
	if r.Status != "accepted" {
		t.Fatalf("旧标识上报应解析后接受，实际 %v", r.RejectReasons)
	}
	obs, _, _, _ := f.svc.GetObservation(r.ObservationID)
	if obs.SubjectRef != "S1" || obs.CanonicalRef != "S2" {
		t.Fatalf("应保留原始标识并解析规范编号，实际 %+v", obs)
	}
	// 重复合并与合并环应被拒绝
	if _, err := f.svc.MergeSubjects("S1", "S2", "重复"); !IsCode(err, "CONFLICT") {
		t.Fatalf("重复合并应被拒绝，实际 %v", err)
	}
	if _, err := f.svc.MergeSubjects("S2", "S1", "成环"); !IsCode(err, "CONFLICT") {
		t.Fatalf("合并成环应被拒绝，实际 %v", err)
	}
}

func error2(_ any, err error) error { return err }

func TestRevisionAndQualityMarkStoredSeparately(t *testing.T) {
	f := setupFixture(t)
	r := uploadOne(t, f.svc, validItem())
	if _, err := f.svc.AddRevision(r.ObservationID, "12.5", "复测修正", "ORG-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AddQualityMark(r.ObservationID, "SUSPECT", "设备漂移待查", "ORG-B"); err != nil {
		t.Fatal(err)
	}
	observation, revisions, marks, err := f.svc.GetObservation(r.ObservationID)
	if err != nil {
		t.Fatal(err)
	}
	if observation.RawValueText != "12.3" {
		t.Fatalf("原始读数被改写: %s", observation.RawValueText)
	}
	if CurrentValue(observation, revisions) != "12.5" {
		t.Fatalf("当前值应取最新修订值")
	}
	if len(marks) != 1 || marks[0].Flag != "SUSPECT" {
		t.Fatalf("质量标记未分别保存")
	}
}

func TestSliceApprovalAndMasking(t *testing.T) {
	f := setupFixture(t)
	r := uploadOne(t, f.svc, validItem())
	if r.Status != "accepted" {
		t.Fatalf("前置上传失败")
	}
	from := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	req, err := f.svc.RequestSlice("ORG-R", "基因组评估", "PIG", []string{"BACKFAT"}, from, to, false)
	if err != nil {
		t.Fatal(err)
	}
	// 未批准不能取数
	if _, err := f.svc.SliceData(req.ID); !IsCode(err, "PERMISSION_DENIED") {
		t.Fatalf("未批准应拒绝取数，实际 %v", err)
	}
	if _, err := f.svc.DecideSlice(req.ID, true, "联合体委员会"); err != nil {
		t.Fatal(err)
	}
	records, err := f.svc.SliceData(req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("期望 1 条切片记录，实际 %d", len(records))
	}
	record := records[0]
	if record.SubjectKey == "S1" || record.SubjectKey == "" {
		t.Fatalf("未授权主体信息应输出假名，实际 %q", record.SubjectKey)
	}
	if record.SireKey != "" || record.DamKey != "" {
		t.Fatalf("未授权主体信息不应输出谱系")
	}
	// 范围外的性状不出现
	req2, err := f.svc.RequestSlice("ORG-R", "其他用途", "PIG", []string{"WEIGHT"}, from, to, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.DecideSlice(req2.ID, true, "联合体委员会"); err != nil {
		t.Fatal(err)
	}
	records2, err := f.svc.SliceData(req2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records2) != 0 {
		t.Fatalf("用途范围外的性状不应输出")
	}
}

func TestFeedbackAndRejectionsVisibleAcrossOrgs(t *testing.T) {
	f := setupFixture(t)
	bad := validItem()
	bad.Unit = "cm"
	r := uploadOne(t, f.svc, bad)
	if r.Status != "rejected" {
		t.Fatalf("应被拒收")
	}
	// 另一机构对该记录提交质量反馈
	if _, err := f.svc.AddFeedback(r.ObservationID, "ORG-C", "WRONG_UNIT", "单位与现行标准不符"); err != nil {
		t.Fatal(err)
	}
	feedback, err := f.svc.FeedbackFor(r.ObservationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(feedback) != 1 || feedback[0].FromOrg != "ORG-C" {
		t.Fatalf("跨机构反馈未保存: %+v", feedback)
	}
	// 上传机构可查询拒收原因
	rejections := f.svc.RejectionsForOrg("ORG-A")
	if len(rejections) != 1 || rejections[0].RejectReasons[0] != "UNIT_MISMATCH" {
		t.Fatalf("拒收原因查询不符合预期: %+v", rejections)
	}
}

func TestProvenanceTracesDatasetToSources(t *testing.T) {
	f := setupFixture(t)
	r := uploadOne(t, f.svc, validItem())
	if _, err := f.svc.AddQualityMark(r.ObservationID, "CHECKED", "", "ORG-A"); err != nil {
		t.Fatal(err)
	}
	dataset, err := f.svc.CreateDataset("2026 秋季背膘分析集", "ORG-R", []string{r.ObservationID})
	if err != nil {
		t.Fatal(err)
	}
	report, err := f.svc.Provenance(dataset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 1 {
		t.Fatalf("溯源报告条目数不符: %d", len(report.Entries))
	}
	entry := report.Entries[0]
	if entry.DeviceSerial != "DEV-1" || entry.CalibrationCertNo != "CAL-2026-001" {
		t.Fatalf("溯源缺少设备或校准证书: %+v", entry)
	}
	if entry.StandardCode != "Q012" || entry.StandardVersion != "1.0" {
		t.Fatalf("溯源缺少规则版本: %+v", entry)
	}
	if entry.RawValueText != "12.3" || len(entry.QualityFlags) != 1 {
		t.Fatalf("溯源缺少原始读数或质量标记: %+v", entry)
	}
	// 被拒观测不能作为数据集来源
	bad := validItem()
	bad.SourceSequence = 31
	bad.Unit = "cm"
	rejected := uploadOne(t, f.svc, bad)
	if _, err := f.svc.CreateDataset("非法数据集", "ORG-R", []string{rejected.ObservationID}); !IsCode(err, "CONFLICT") {
		t.Fatalf("被拒观测不应进入数据集，实际 %v", err)
	}
}

func TestPersistenceAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, func() time.Time { return testNow })
	standard, err := svc.RegisterStandard("Q012", "1.0", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	// 重新打开后数据仍在
	st2, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc2 := New(st2, func() time.Time { return testNow })
	if _, err := svc2.ActivateStandard(standard.ID); err != nil {
		t.Fatalf("重启后标准版本丢失: %v", err)
	}
}
