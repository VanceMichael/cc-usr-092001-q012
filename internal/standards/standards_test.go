package standards_test

import (
	"testing"
	"time"

	"example.com/batch-092001-q012/internal/apperr"
	"example.com/batch-092001-q012/internal/canon"
	"example.com/batch-092001-q012/internal/domain"
	"example.com/batch-092001-q012/internal/standards"
	"example.com/batch-092001-q012/internal/store"
	"example.com/batch-092001-q012/internal/testkit"
)

func baseRules(decimals int) domain.RuleSet {
	return domain.RuleSet{
		Species: []string{"poultry"},
		Traits: []domain.TraitDef{{
			Code: "body_weight", Species: "poultry", Name: "体重", Unit: "kg", Decimals: decimals,
			MinValue: canon.MustDecimal("0.01"), MaxValue: canon.MustDecimal("20"),
		}},
		AllowedDeviceKinds:  []string{"platform_scale"},
		MaxClockSkewMinutes: 30,
	}
}

func TestRegisterFreezesRulesHashAndIsImmutable(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s := store.New()
	svc := standards.New(s, clock.Now)

	v1, err := svc.Register(standards.RegisterInput{
		StandardID: "S", Version: "2026.1", EffectiveAt: clock.Now().AddDate(0, -1, 0),
		Rules: baseRules(3), RegisteredBy: "C",
	})
	if err != nil {
		t.Fatalf("登记 v1 失败: %v", err)
	}
	hashAtRegister := v1.RulesHash

	// 同版本号同内容也拒绝：冻结后没有"再登记"通道。
	if _, err := svc.Register(standards.RegisterInput{
		StandardID: "S", Version: "2026.1", EffectiveAt: v1.EffectiveAt, Rules: baseRules(3),
	}); err == nil {
		t.Fatal("同版本号重复登记应冲突")
	}

	// 规则哈希是登记时对正文的快照：即便内存对象被篡改，存根哈希也不变。
	stored, err := svc.Get("S", "2026.1")
	if err != nil {
		t.Fatal(err)
	}
	stored.Rules.Traits[0].Decimals = 5
	fresh, _ := svc.Get("S", "2026.1")
	if fresh.RulesHash != hashAtRegister {
		t.Fatal("规则哈希不应随对象字段变化而改变")
	}
}

func TestEffectiveAtPicksVersionActiveAtOccurrence(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC))
	w := testkit.New(clock)
	testkit.Seed(t, w)

	// 在 2027-01-01 让 v2 生效（收紧精度到 2 位）。
	v2At := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	v2, err := w.Standard.Register(standards.RegisterInput{
		StandardID: testkit.StandardID, Version: "2027.1", EffectiveAt: v2At,
		Rules: baseRules(2), RegisteredBy: "C",
	})
	if err != nil {
		t.Fatalf("登记 v2 失败: %v", err)
	}

	// 2026-06 的旧采集仍解析到 v1；2027-06 的新采集解析到 v2。
	old, err := w.Standard.EffectiveAt(testkit.StandardID, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if old.Version != "2026.1" {
		t.Fatalf("旧采集应绑定 v1，实际 %s", old.Version)
	}
	newer, err := w.Standard.EffectiveAt(testkit.StandardID, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if newer.RulesHash != v2.RulesHash {
		t.Fatal("新采集应绑定 v2 的规则哈希")
	}
}

func TestNewVersionEffectiveTimeMustAdvance(t *testing.T) {
	clock := testkit.NewFakeClock(time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC))
	w := testkit.New(clock)
	testkit.Seed(t, w)

	v1, _ := w.Standard.Get(testkit.StandardID, "2026.1")
	_, err := w.Standard.Register(standards.RegisterInput{
		StandardID: testkit.StandardID, Version: "2027.9",
		EffectiveAt: v1.EffectiveAt, // 与旧版本同时刻
		Rules:       baseRules(2),
	})
	if ae, ok := err.(*apperr.Error); !ok || ae.Code != apperr.Validation {
		t.Fatalf("生效时间不晚于既有版本应被校验拒绝，实际: %v", err)
	}
}
