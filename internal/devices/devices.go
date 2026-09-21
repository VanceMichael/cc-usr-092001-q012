// Package devices 负责测定设备登记、适用范围与校准证书管理。
//
// 校准证书是采集合法性的前置条件：采集时刻必须落在最新证书的
// issued..expires 区间内，且证书未被吊销；超过 due 仅产生告警质量标记，
// 超过 expires 或被吊销则直接拒收。
package devices

import (
	"strings"
	"time"

	"example.com/batch-092001-q012/internal/apperr"
	"example.com/batch-092001-q012/internal/canon"
	"example.com/batch-092001-q012/internal/domain"
	"example.com/batch-092001-q012/internal/store"
)

// Service 是设备与校准服务。
type Service struct {
	store *store.Store
	now   func() time.Time
}

// New 创建设备服务。
func New(s *store.Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: s, now: now}
}

// RegisterInput 登记设备入参。
type RegisterInput struct {
	Serial      string
	Kind        string
	VendorModel string
	OwnerOrgID  string
	Species     []string
	TraitCodes  []string
}

// Register 登记设备。序列号是设备的稳定身份，重复登记视为冲突。
func (svc *Service) Register(in RegisterInput) (*domain.Device, error) {
	if in.Serial == "" || in.Kind == "" || in.OwnerOrgID == "" {
		return nil, apperr.New(apperr.Validation, "设备序列号、类型与归属机构不能为空")
	}
	if len(in.Species) == 0 || len(in.TraitCodes) == 0 {
		return nil, apperr.New(apperr.Validation, "设备必须声明适用物种与性状范围")
	}
	d := &domain.Device{
		Serial:       strings.TrimSpace(in.Serial),
		Kind:         in.Kind,
		VendorModel:  in.VendorModel,
		OwnerOrgID:   in.OwnerOrgID,
		Species:      append([]string(nil), in.Species...),
		TraitCodes:   append([]string(nil), in.TraitCodes...),
		RegisteredAt: svc.now(),
		Active:       true,
	}
	err := svc.store.Update(func() error {
		if owner, ok := svc.store.GetOrg(in.OwnerOrgID); !ok || !owner.Active {
			return apperr.New(apperr.Validation, "设备归属机构 %s 不存在或已停用", in.OwnerOrgID)
		}
		if _, exists := svc.store.GetDevice(d.Serial); exists {
			return apperr.New(apperr.Conflict, "设备序列号 %s 已登记", d.Serial)
		}
		svc.store.PutDevice(d)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

// AddCalibrationInput 上传校准证书入参。
type AddCalibrationInput struct {
	DeviceSerial   string
	IssuedAt       time.Time
	DueAt          time.Time
	ExpiresAt      time.Time
	CertRef        string
	CertDigest     string
	IssuedBy       string
	MaxUncertainty map[string]string // 性状编码 -> 字符串十进制
}

// AddCalibration 登记校准证书。
func (svc *Service) AddCalibration(in AddCalibrationInput) (*domain.Calibration, error) {
	if in.DeviceSerial == "" || in.CertRef == "" || in.CertDigest == "" {
		return nil, apperr.New(apperr.Validation, "设备序列号、证书引用与摘要是必填项")
	}
	if !strings.HasPrefix(in.CertDigest, "sha256:") {
		return nil, apperr.New(apperr.Validation, "证书摘要必须为 sha256: 前缀")
	}
	if in.IssuedAt.IsZero() || in.ExpiresAt.IsZero() {
		return nil, apperr.New(apperr.Validation, "校准签发时间与失效时间不能为空")
	}
	if !in.ExpiresAt.After(in.IssuedAt) {
		return nil, apperr.New(apperr.Validation, "校准失效时间必须晚于签发时间")
	}
	if !in.DueAt.IsZero() && (in.DueAt.Before(in.IssuedAt) || in.DueAt.After(in.ExpiresAt)) {
		return nil, apperr.New(apperr.Validation, "复校时间应介于签发时间与失效时间之间")
	}
	uncertainty := map[string]canon.Decimal{}
	for trait, text := range in.MaxUncertainty {
		d, err := canon.ParseDecimal(text)
		if err != nil {
			return nil, apperr.New(apperr.Validation, "性状 %s 的不确定度上限无效: %v", trait, err)
		}
		uncertainty[trait] = d
	}
	c := &domain.Calibration{
		ID:             store.NewID("CAL"),
		DeviceSerial:   strings.TrimSpace(in.DeviceSerial),
		IssuedAt:       in.IssuedAt,
		DueAt:          in.DueAt,
		ExpiresAt:      in.ExpiresAt,
		CertRef:        in.CertRef,
		CertDigest:     in.CertDigest,
		IssuedBy:       in.IssuedBy,
		MaxUncertainty: uncertainty,
	}
	err := svc.store.Update(func() error {
		d, ok := svc.store.GetDevice(c.DeviceSerial)
		if !ok {
			return apperr.New(apperr.NotFound, "设备 %s 未登记", c.DeviceSerial)
		}
		if !d.Active {
			return apperr.New(apperr.Validation, "设备 %s 已停用", c.DeviceSerial)
		}
		svc.store.PutCalibration(c)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// RevokeCalibration 吊销证书（如发现证书造假）。吊销时刻之后的采集立即失效。
func (svc *Service) RevokeCalibration(calibrationID, reason string) error {
	if reason == "" {
		return apperr.New(apperr.Validation, "吊销校准证书必须填写原因")
	}
	return svc.store.Update(func() error {
		c, ok := svc.store.GetCalibration(calibrationID)
		if !ok {
			return apperr.New(apperr.NotFound, "校准证书 %s 不存在", calibrationID)
		}
		if c.RevokedAt != nil {
			return apperr.New(apperr.Conflict, "校准证书 %s 已吊销", calibrationID)
		}
		at := svc.now()
		c.RevokedAt = &at
		c.RevokeReason = reason
		return nil
	})
}

// Deactivate 停用设备（停用后新采集拒收，历史记录保留）。
func (svc *Service) Deactivate(serial string) error {
	return svc.store.Update(func() error {
		d, ok := svc.store.GetDevice(serial)
		if !ok {
			return apperr.New(apperr.NotFound, "设备 %s 未登记", serial)
		}
		d.Active = false
		return nil
	})
}

// GetDevice 读取设备登记。
func (svc *Service) GetDevice(serial string) (*domain.Device, error) {
	var out *domain.Device
	err := svc.store.View(func() error {
		d, ok := svc.store.GetDevice(serial)
		if !ok {
			return apperr.New(apperr.NotFound, "设备 %s 未登记", serial)
		}
		out = d
		return nil
	})
	return out, err
}

// InScope 判断设备是否声明覆盖某物种与性状。
func InScope(d *domain.Device, species, traitCode string) bool {
	speciesOK := false
	for _, sp := range d.Species {
		if sp == species {
			speciesOK = true
			break
		}
	}
	if !speciesOK {
		return false
	}
	for _, code := range d.TraitCodes {
		if code == traitCode {
			return true
		}
	}
	return false
}

// CalibrationAt 查询设备在某时刻生效的证书与状态。
func (svc *Service) CalibrationAt(serial string, at time.Time) (*domain.Calibration, string, error) {
	var c *domain.Calibration
	var status string
	err := svc.store.View(func() error {
		found, st, ok := svc.store.CalibrationAt(serial, at)
		if !ok {
			return apperr.New(apperr.CalibrationBad, "设备 %s 在 %s 没有任何校准证书", serial, at)
		}
		c, status = found, st
		return nil
	})
	return c, status, err
}
