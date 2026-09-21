package subjects_test

import (
	"testing"
	"time"

	"example.com/batch-092001-q012/internal/apperr"
	"example.com/batch-092001-q012/internal/subjects"
	"example.com/batch-092001-q012/internal/testkit"
)

func TestMergeKeepsChainResolvableAndRewritesEdges(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC))
	w := testkit.New(clock)
	testkit.Seed(t, w)

	// 建立子代 CHILD，母本指向 HEN-DUP（即将被合并的个体）。
	dob := clock.Now().AddDate(0, 0, -30)
	if _, err := w.Subject.Register(subjects.RegisterInput{
		ID: "CHILD-1", Species: "poultry", Dob: &dob,
		DamID: testkit.HenDup, Generation: 3, Sex: "M", ByOrgID: testkit.FarmID,
	}); err != nil {
		t.Fatalf("登记子代失败: %v", err)
	}
	if err := w.Subject.BindAlias(testkit.FarmID, "TAG-DUP", testkit.HenDup); err != nil {
		t.Fatal(err)
	}

	// 执行合并 HEN-DUP -> HEN-A。
	if err := w.Subject.Merge(testkit.HenDup, testkit.HenA, "同一羽鸡重复建档", testkit.FarmID); err != nil {
		t.Fatalf("合并失败: %v", err)
	}

	// 1) 旧编号仍可解析到存续个体。
	got, err := w.Subject.Resolve("", testkit.HenDup)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != testkit.HenA {
		t.Fatalf("旧编号应解析到 HEN-A，实际 %s", got.ID)
	}
	// 2) 机构本地号跟随迁移。
	got, err = w.Subject.Resolve(testkit.FarmID, "TAG-DUP")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != testkit.HenA {
		t.Fatalf("本地别名应迁移到 HEN-A，实际 %s", got.ID)
	}
	// 3) 子代亲子边已改写，不悬空。
	child, err := w.Subject.Get("CHILD-1")
	if err != nil {
		t.Fatal(err)
	}
	if child.DamID != testkit.HenA {
		t.Fatalf("子代母本边应改写为 HEN-A，实际 %s", child.DamID)
	}
	// 4) 被合并个体以墓碑形式保留。
	tomb, err := w.Subject.Get(testkit.HenDup)
	if err != nil {
		t.Fatal(err)
	}
	if tomb.MergedInto != testkit.HenA || tomb.Active {
		t.Fatal("被合并个体应是指向 HEN-A 的失活墓碑")
	}
	// 5) 亲缘链改写历史可查。
	history, err := w.Subject.LineageHistory("CHILD-1")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range history {
		if e.Kind == "correction" && e.OldDam == testkit.HenDup && e.NewDam == testkit.HenA {
			found = true
		}
	}
	if !found {
		t.Fatal("谱系改写事件未留痕")
	}
}

func TestMergeAncestorDescendantRejected(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC))
	w := testkit.New(clock)
	testkit.Seed(t, w)

	dob := clock.Now().AddDate(0, 0, -30)
	if _, err := w.Subject.Register(subjects.RegisterInput{
		ID: "CHILD-2", Species: "poultry", Dob: &dob,
		DamID: testkit.HenA, Generation: 3, Sex: "F", ByOrgID: testkit.FarmID,
	}); err != nil {
		t.Fatal(err)
	}
	// 祖先并入后代。
	err := w.Subject.Merge(testkit.HenA, "CHILD-2", "错误合并", testkit.FarmID)
	if ae, ok := err.(*apperr.Error); !ok || ae.Code != apperr.LineageCycle {
		t.Fatalf("祖先/后代合并应以谱系成环拒绝，实际: %v", err)
	}
	// 后代并入祖先同样拒绝。
	err = w.Subject.Merge("CHILD-2", testkit.HenA, "反向错误合并", testkit.FarmID)
	if ae, ok := err.(*apperr.Error); !ok || ae.Code != apperr.LineageCycle {
		t.Fatalf("后代并入祖先也应拒绝，实际: %v", err)
	}
}

func TestLineageCorrectionCycleRejected(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC))
	w := testkit.New(clock)
	testkit.Seed(t, w)

	// HEN-B 的母本设为 HEN-A，再把 HEN-A 的母本改成 HEN-B 即成环。
	if err := w.Subject.CorrectLineage(testkit.HenB, nil, strPtr(testkit.HenA), "补登母本", testkit.FarmID); err != nil {
		t.Fatal(err)
	}
	err := w.Subject.CorrectLineage(testkit.HenA, nil, strPtr(testkit.HenB), "制造环", testkit.FarmID)
	if ae, ok := err.(*apperr.Error); !ok || ae.Code != apperr.LineageCycle {
		t.Fatalf("应检测到谱系成环，实际: %v", err)
	}
}

func strPtr(s string) *string { return &s }

func TestAliasCorrectionKeepsAuditTrail(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC))
	w := testkit.New(clock)
	testkit.Seed(t, w)

	// TAG-100 在种子里绑到 HEN-A，纠错改绑 HEN-B。
	if err := w.Subject.CorrectAlias(testkit.FarmID, "TAG-100", testkit.HenB, "翅号录入错误"); err != nil {
		t.Fatal(err)
	}
	got, err := w.Subject.Resolve(testkit.FarmID, "TAG-100")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != testkit.HenB {
		t.Fatalf("纠错后应解析到 HEN-B，实际 %s", got.ID)
	}
	// 无纠错原因的直接改绑应被拒绝。
	if err := w.Subject.BindAlias(testkit.FarmID, "TAG-100", testkit.HenA); err == nil {
		t.Fatal("已绑定别名直接改绑应拒绝，必须走纠错")
	}
}
