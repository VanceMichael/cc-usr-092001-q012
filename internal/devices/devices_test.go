package devices_test

import (
	"testing"
	"time"

	"example.com/batch-092001-q012/internal/devices"
	"example.com/batch-092001-q012/internal/domain"
	"example.com/batch-092001-q012/internal/testkit"
)

func TestCalibrationLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(now)
	w := testkit.New(clock)
	testkit.Seed(t, w)

	// 种子证书在当前时刻有效。
	cal, status, err := w.Device.CalibrationAt(testkit.ScaleSerial, now)
	if err != nil {
		t.Fatal(err)
	}
	if status != domain.CalibrationValid {
		t.Fatalf("当前证书应为 valid，实际 %s", status)
	}

	// 越过 due 但未过 expires：临期。
	_, status, err = w.Device.CalibrationAt(testkit.ScaleSerial, cal.DueAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if status != domain.CalibrationDue {
		t.Fatalf("过复校期应判 due，实际 %s", status)
	}

	// 越过 expires：失效。
	_, status, err = w.Device.CalibrationAt(testkit.ScaleSerial, cal.ExpiresAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if status != domain.CalibrationDead {
		t.Fatalf("过失效期应判 expired，实际 %s", status)
	}

	// 补一张新证书后，新时刻应取新证书，旧时刻仍取旧证书。
	newCal, err := w.Device.AddCalibration(newCalibration(testkit.ScaleSerial, cal.ExpiresAt.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := w.Device.CalibrationAt(testkit.ScaleSerial, cal.ExpiresAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != newCal.ID {
		t.Fatal("新证书应覆盖失效后的时刻")
	}
	gotOld, _, _ := w.Device.CalibrationAt(testkit.ScaleSerial, now)
	if gotOld.ID != cal.ID {
		t.Fatal("历史时刻仍应解析到旧证书")
	}

	// 吊销：吊销时刻后立即不可用。
	if err := w.Device.RevokeCalibration(newCal.ID, "发现问题"); err != nil {
		t.Fatal(err)
	}
	_, status, err = w.Device.CalibrationAt(testkit.ScaleSerial, cal.ExpiresAt.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if status != domain.CalibrationRevoked {
		t.Fatalf("吊销后应判 revoked，实际 %s", status)
	}
}

func TestRejectDuplicateSerialAndUnknownOwner(t *testing.T) {
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(now)
	w := testkit.New(clock)
	testkit.Seed(t, w)

	if _, err := w.Device.Register(regInput(testkit.ScaleSerial, testkit.FarmID)); err == nil {
		t.Fatal("重复序列号应拒绝")
	}
	if _, err := w.Device.Register(regInput("SCALE-X", "GHOST")); err == nil {
		t.Fatal("未知机构设备应拒绝")
	}
}

func regInput(serial, owner string) devices.RegisterInput {
	return devices.RegisterInput{
		Serial:     serial,
		Kind:       "platform_scale",
		OwnerOrgID: owner,
		Species:    []string{"poultry"},
		TraitCodes: []string{"body_weight"},
	}
}

func newCalibration(serial string, issued time.Time) devices.AddCalibrationInput {
	return devices.AddCalibrationInput{
		DeviceSerial:   serial,
		IssuedAt:       issued,
		DueAt:          issued.AddDate(0, 0, 30),
		ExpiresAt:      issued.AddDate(0, 0, 200),
		CertRef:        "CERT-NEW",
		CertDigest:     "sha256:newcert",
		IssuedBy:       "计量院",
		MaxUncertainty: map[string]string{"body_weight": "0.01"},
	}
}
