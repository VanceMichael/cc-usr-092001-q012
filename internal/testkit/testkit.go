// Package testkit 为各服务包测试装配一套带种子数据的内存世界。
package testkit

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"example.com/batch-092001-q012/internal/canon"
	"example.com/batch-092001-q012/internal/devices"
	"example.com/batch-092001-q012/internal/domain"
	"example.com/batch-092001-q012/internal/ingest"
	"example.com/batch-092001-q012/internal/provenance"
	"example.com/batch-092001-q012/internal/quality"
	"example.com/batch-092001-q012/internal/records"
	"example.com/batch-092001-q012/internal/slices"
	"example.com/batch-092001-q012/internal/standards"
	"example.com/batch-092001-q012/internal/store"
	"example.com/batch-092001-q012/internal/subjects"
)

const (
	FarmID      = "FARM1"
	InstituteID = "INST1"
	OtherFarm   = "FARM2"
	StandardID  = "STD-LIVESTOCK"
	ScaleSerial = "SCALE-1"
	UltraSerial = "ULTRA-1"
	HenA        = "HEN-A"
	HenB        = "HEN-B"
	HenDup      = "HEN-DUP"
	Pig1        = "PIG-1"
)

// FakeClock 是测试可控时钟。
type FakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{t: t} }
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// World 持有全部服务。
type World struct {
	Clock    *FakeClock
	Store    *store.Store
	Standard *standards.Service
	Device   *devices.Service
	Subject  *subjects.Service
	Ingest   *ingest.Service
	Record   *records.Service
	Quality  *quality.Service
	Slice    *slices.Service
	Prov     *provenance.Service
}

// New 装配空世界。
func New(clock *FakeClock) *World {
	s := store.New()
	w := &World{
		Clock:    clock,
		Store:    s,
		Standard: standards.New(s, clock.Now),
		Device:   devices.New(s, clock.Now),
		Subject:  subjects.New(s, clock.Now),
		Record:   records.New(s, clock.Now),
		Quality:  quality.New(s, clock.Now),
		Slice:    slices.New(s, clock.Now),
		Prov:     provenance.New(s, clock.Now),
	}
	w.Ingest = ingest.New(s, w.Standard, w.Device, w.Subject, clock.Now)
	return w
}

// Seed 注册一套可用的基础数据：两家养殖企业、一家研究机构、标准 v1、
// 两台设备及有效校准、若干个体与别名。
func Seed(t *testing.T, w *World) {
	t.Helper()
	now := w.Clock.Now()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("种子数据失败: %v", err)
		}
	}
	must(w.Store.Update(func() error {
		w.Store.PutOrg(&domain.Org{ID: FarmID, Name: "示范种禽场", Role: "farm", Active: true, RegisteredAt: now})
		w.Store.PutOrg(&domain.Org{ID: OtherFarm, Name: "另一猪场", Role: "farm", Active: true, RegisteredAt: now})
		w.Store.PutOrg(&domain.Org{ID: InstituteID, Name: "畜牧研究院", Role: "institute", Active: true, RegisteredAt: now})
		return nil
	}))

	rules := domain.RuleSet{
		Species: []string{"poultry", "swine"},
		Traits: []domain.TraitDef{
			{
				Code: "body_weight", Species: "poultry", Name: "体重", Unit: "kg",
				Decimals: 3, MinValue: canon.MustDecimal("0.01"), MaxValue: canon.MustDecimal("20"),
				MinAgeDays: 1, MaxAgeDays: 120,
			},
			{
				Code: "backfat_thickness", Species: "swine", Name: "背膘厚", Unit: "mm",
				Decimals: 2, MinValue: canon.MustDecimal("1"), MaxValue: canon.MustDecimal("80"),
				MinAgeDays: 60, MaxAgeDays: 400,
			},
		},
		Protocols: []domain.ProtocolStep{
			{Order: 1, Action: "fasten", Requirement: "禁食 4 小时"},
			{Order: 2, Action: "weigh", Requirement: "连续两次读数差 < 1%"},
		},
		Formats: []domain.FormatRule{
			{Field: "value", Type: "decimal-string", Required: true},
		},
		AllowedDeviceKinds:  []string{"platform_scale", "ultrasound"},
		MaxClockSkewMinutes: 60,
		MaxBatchLagHours:    24 * 30,
	}
	_, err := w.Standard.Register(standards.RegisterInput{
		StandardID:   StandardID,
		Version:      "2026.1",
		EffectiveAt:  now.AddDate(-1, 0, 0),
		Rules:        rules,
		RegisteredBy: "CONSORTIUM",
	})
	must(err)

	_, err = w.Device.Register(devices.RegisterInput{
		Serial: ScaleSerial, Kind: "platform_scale", VendorModel: "ACME S-100",
		OwnerOrgID: FarmID, Species: []string{"poultry", "swine"}, TraitCodes: []string{"body_weight"},
	})
	must(err)
	_, err = w.Device.Register(devices.RegisterInput{
		Serial: UltraSerial, Kind: "ultrasound", VendorModel: "ACME U-200",
		OwnerOrgID: OtherFarm, Species: []string{"swine"}, TraitCodes: []string{"backfat_thickness"},
	})
	must(err)

	_, err = w.Device.AddCalibration(devices.AddCalibrationInput{
		DeviceSerial: ScaleSerial,
		IssuedAt:     now.AddDate(0, 0, -200),
		DueAt:        now.AddDate(0, 0, 30),
		ExpiresAt:    now.AddDate(0, 0, 100),
		CertRef:      "CERT-SCALE-1", CertDigest: "sha256:scalecert", IssuedBy: "计量院",
		MaxUncertainty: map[string]string{"body_weight": "0.01"},
	})
	must(err)
	_, err = w.Device.AddCalibration(devices.AddCalibrationInput{
		DeviceSerial: UltraSerial,
		IssuedAt:     now.AddDate(0, 0, -100),
		DueAt:        now.AddDate(0, 0, 200),
		ExpiresAt:    now.AddDate(0, 0, 300),
		CertRef:      "CERT-ULTRA-1", CertDigest: "sha256:ultracert", IssuedBy: "计量院",
		MaxUncertainty: map[string]string{"backfat_thickness": "0.5"},
	})
	must(err)

	dob := now.AddDate(0, 0, -60)
	_, err = w.Subject.Register(subjects.RegisterInput{
		ID: HenA, Species: "poultry", Dob: &dob, Generation: 2, Sex: "F", ByOrgID: FarmID,
	})
	must(err)
	_, err = w.Subject.Register(subjects.RegisterInput{
		ID: HenB, Species: "poultry", Dob: &dob, Generation: 2, Sex: "F", ByOrgID: FarmID,
	})
	must(err)
	_, err = w.Subject.Register(subjects.RegisterInput{
		ID: HenDup, Species: "poultry", Dob: &dob, Generation: 2, Sex: "F", ByOrgID: FarmID,
	})
	must(err)
	pigDob := now.AddDate(0, 0, -200)
	_, err = w.Subject.Register(subjects.RegisterInput{
		ID: Pig1, Species: "swine", Dob: &pigDob, Generation: 3, Sex: "M", ByOrgID: OtherFarm,
	})
	must(err)
	must(w.Subject.BindAlias(FarmID, "TAG-100", HenA))
}

// ItemOpts 构造采集条目的参数。
type ItemOpts struct {
	Seq         int64
	AnimalRef   string
	Species     string
	Trait       string
	Value       string
	Unit        string
	Uncertainty string
	OccurredAt  time.Time
	ProtocolRef string
}

// MakeItem 构造带正确 payload_digest 的原始 JSON 条目。
func MakeItem(o ItemOpts) json.RawMessage {
	m := map[string]any{
		"source_sequence": o.Seq,
		"animal_ref":      o.AnimalRef,
		"species":         o.Species,
		"trait_code":      o.Trait,
		"value":           o.Value,
		"unit":            o.Unit,
		"occurred_at":     o.OccurredAt.Format(time.RFC3339),
		"protocol_ref":    o.ProtocolRef,
	}
	if o.Uncertainty != "" {
		m["uncertainty"] = o.Uncertainty
	}
	digest, err := canon.Hash(m)
	if err != nil {
		panic(err)
	}
	m["payload_digest"] = digest
	raw, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return raw
}
