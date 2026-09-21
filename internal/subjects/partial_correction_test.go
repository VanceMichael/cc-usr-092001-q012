package subjects_test

import (
	"testing"
	"time"

	"example.com/batch-092001-q012/internal/subjects"
	"example.com/batch-092001-q012/internal/testkit"
)

// 只提交母本纠错（sire_id 省略为 nil）时，既有父本边必须保留。
func TestPartialLineageCorrectionKeepsOtherParent(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC))
	w := testkit.New(clock)
	testkit.Seed(t, w)

	dob := clock.Now().AddDate(0, 0, -20)
	// CHILD 初始父本 HEN-A 在此测试里仅借用同物种编号；谱系上性别不做强制。
	if _, err := w.Subject.Register(subjects.RegisterInput{
		ID: "CHILD-9", Species: "poultry", Dob: &dob,
		SireID: testkit.HenA, DamID: testkit.HenB, Generation: 3, Sex: "F", ByOrgID: testkit.FarmID,
	}); err != nil {
		t.Fatal(err)
	}
	// 只改母本：传 nil 给父本。
	dam := testkit.HenDup
	if err := w.Subject.CorrectLineage("CHILD-9", nil, &dam, "母本登记错误", testkit.FarmID); err != nil {
		t.Fatal(err)
	}
	got, err := w.Subject.Get("CHILD-9")
	if err != nil {
		t.Fatal(err)
	}
	if got.SireID != testkit.HenA {
		t.Fatalf("未提交的父本边应保留，实际 %q", got.SireID)
	}
	if got.DamID != testkit.HenDup {
		t.Fatalf("母本边应改为 HEN-DUP，实际 %q", got.DamID)
	}
}
