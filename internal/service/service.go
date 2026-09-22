// Package service 实现接入网关的业务规则。
package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"example.com/batch-092001-q012/internal/domain"
	"example.com/batch-092001-q012/internal/store"
)

// Error 是带稳定代码的业务错误，供接口层映射状态码。
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

func fail(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// IsCode 判断 err 是否为指定代码的业务错误。
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// Service 承载全部业务规则。
type Service struct {
	st  *store.Store
	now func() time.Time
}

// New 基于存储创建服务；now 为 nil 时使用系统时间。
func New(st *store.Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, now: now}
}

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + hex.EncodeToString(b[:])
}

// ---- 标准版本与登记 ----

// RegisterStandard 登记一个标准版本，初始为草案状态。
func (s *Service) RegisterStandard(code, version string, effectiveFrom time.Time) (domain.StandardVersion, error) {
	if code == "" || version == "" {
		return domain.StandardVersion{}, fail("INVALID_ARGUMENT", "标准代码与版本号不能为空")
	}
	s.st.Lock()
	defer s.st.Unlock()
	for _, existing := range s.st.Standards {
		if existing.Code == code && existing.Version == version {
			return domain.StandardVersion{}, fail("CONFLICT", "标准 %s 版本 %s 已登记", code, version)
		}
	}
	standard := domain.StandardVersion{
		ID:            newID("STD"),
		Code:          code,
		Version:       version,
		Status:        domain.StandardDraft,
		EffectiveFrom: effectiveFrom,
		RegisteredAt:  s.now(),
	}
	s.st.Standards[standard.ID] = standard
	return standard, s.st.Save()
}

// ActivateStandard 使标准版本生效；生效后其下规则冻结，只能登记新版本。
func (s *Service) ActivateStandard(id string) (domain.StandardVersion, error) {
	s.st.Lock()
	defer s.st.Unlock()
	standard, ok := s.st.Standards[id]
	if !ok {
		return domain.StandardVersion{}, fail("NOT_FOUND", "标准版本 %s 不存在", id)
	}
	if standard.Status != domain.StandardDraft {
		return domain.StandardVersion{}, fail("CONFLICT", "只有草案状态的标准可以生效")
	}
	standard.Status = domain.StandardEffective
	s.st.Standards[id] = standard
	return standard, s.st.Save()
}

// RetireStandard 废止标准版本；废止只影响生效时间之后的新采集，
// 已按该版本保存的数据保持原样，不做重释。
func (s *Service) RetireStandard(id string, at time.Time) (domain.StandardVersion, error) {
	s.st.Lock()
	defer s.st.Unlock()
	standard, ok := s.st.Standards[id]
	if !ok {
		return domain.StandardVersion{}, fail("NOT_FOUND", "标准版本 %s 不存在", id)
	}
	if standard.Status != domain.StandardEffective {
		return domain.StandardVersion{}, fail("CONFLICT", "只有生效中的标准可以废止")
	}
	if !at.After(standard.EffectiveFrom) {
		return domain.StandardVersion{}, fail("INVALID_ARGUMENT", "废止时间必须晚于生效时间")
	}
	standard.Status = domain.StandardRetired
	standard.RetiredAt = &at
	s.st.Standards[id] = standard
	return standard, s.st.Save()
}

// ResolveStandard 解析 occurredAt 时刻适用的标准版本：
// 已生效、生效时间不晚于 occurredAt、且未在 occurredAt 前废止的版本中，取生效时间最新者。
func (s *Service) ResolveStandard(occurredAt time.Time) (domain.StandardVersion, bool) {
	var best domain.StandardVersion
	found := false
	for _, standard := range s.st.Standards {
		if standard.Status != domain.StandardEffective && standard.Status != domain.StandardRetired {
			continue
		}
		if standard.EffectiveFrom.After(occurredAt) {
			continue
		}
		if standard.RetiredAt != nil && !occurredAt.Before(*standard.RetiredAt) {
			continue
		}
		if !found || standard.EffectiveFrom.After(best.EffectiveFrom) {
			best = standard
			found = true
		}
	}
	return best, found
}

// requireDraftStandard 校验标准存在且处于草案（可登记规则）状态。
func (s *Service) requireDraftStandard(standardID string) (domain.StandardVersion, error) {
	standard, ok := s.st.Standards[standardID]
	if !ok {
		return domain.StandardVersion{}, fail("NOT_FOUND", "标准版本 %s 不存在", standardID)
	}
	if standard.Status != domain.StandardDraft {
		return domain.StandardVersion{}, fail("CONFLICT", "标准 %s 版本 %s 已生效或废止，规则不可再修改；请登记新版本", standard.Code, standard.Version)
	}
	return standard, nil
}

// RegisterSpecies 在标准版本下登记物种。
func (s *Service) RegisterSpecies(standardID, code, name string) (domain.Species, error) {
	if code == "" {
		return domain.Species{}, fail("INVALID_ARGUMENT", "物种代码不能为空")
	}
	s.st.Lock()
	defer s.st.Unlock()
	if _, err := s.requireDraftStandard(standardID); err != nil {
		return domain.Species{}, err
	}
	key := standardID + "|" + code
	if _, exists := s.st.Species[key]; exists {
		return domain.Species{}, fail("CONFLICT", "物种 %s 在该标准版本下已登记", code)
	}
	species := domain.Species{Code: code, Name: name, StandardID: standardID}
	s.st.Species[key] = species
	return species, s.st.Save()
}

// RegisterTrait 在标准版本下登记性状定义。
func (s *Service) RegisterTrait(standardID, speciesCode, code, name, unit string, precision int, minValue, maxValue *float64) (domain.TraitDefinition, error) {
	if code == "" || unit == "" {
		return domain.TraitDefinition{}, fail("INVALID_ARGUMENT", "性状代码与单位不能为空")
	}
	if precision < 0 {
		return domain.TraitDefinition{}, fail("INVALID_ARGUMENT", "精度不能为负数")
	}
	s.st.Lock()
	defer s.st.Unlock()
	if _, err := s.requireDraftStandard(standardID); err != nil {
		return domain.TraitDefinition{}, err
	}
	if _, ok := s.st.Species[standardID+"|"+speciesCode]; !ok {
		return domain.TraitDefinition{}, fail("NOT_FOUND", "物种 %s 未在该标准版本下登记", speciesCode)
	}
	key := standardID + "|" + speciesCode + "|" + code
	if _, exists := s.st.Traits[key]; exists {
		return domain.TraitDefinition{}, fail("CONFLICT", "性状 %s 在该物种下已登记", code)
	}
	trait := domain.TraitDefinition{
		ID:          newID("TRT"),
		StandardID:  standardID,
		SpeciesCode: speciesCode,
		Code:        code,
		Name:        name,
		Unit:        unit,
		Precision:   precision,
		MinValue:    minValue,
		MaxValue:    maxValue,
	}
	s.st.Traits[key] = trait
	return trait, s.st.Save()
}

// RegisterProtocol 在标准版本下登记采集流程（日龄窗口）。
func (s *Service) RegisterProtocol(standardID, speciesCode, traitCode string, minAgeDays, maxAgeDays int) (domain.CollectionProtocol, error) {
	if minAgeDays < 0 || maxAgeDays < minAgeDays {
		return domain.CollectionProtocol{}, fail("INVALID_ARGUMENT", "日龄窗口不合法")
	}
	s.st.Lock()
	defer s.st.Unlock()
	if _, err := s.requireDraftStandard(standardID); err != nil {
		return domain.CollectionProtocol{}, err
	}
	traitKey := standardID + "|" + speciesCode + "|" + traitCode
	if _, ok := s.st.Traits[traitKey]; !ok {
		return domain.CollectionProtocol{}, fail("NOT_FOUND", "性状 %s 未在该标准版本下登记", traitCode)
	}
	key := traitKey
	if _, exists := s.st.Protocols[key]; exists {
		return domain.CollectionProtocol{}, fail("CONFLICT", "该性状的采集流程已登记")
	}
	protocol := domain.CollectionProtocol{
		ID:          newID("PRT"),
		StandardID:  standardID,
		SpeciesCode: speciesCode,
		TraitCode:   traitCode,
		MinAgeDays:  minAgeDays,
		MaxAgeDays:  maxAgeDays,
	}
	s.st.Protocols[key] = protocol
	return protocol, s.st.Save()
}

// RegisterFormat 在标准版本下登记可接受的数据格式。
func (s *Service) RegisterFormat(standardID, code, version, description string) (domain.DataFormat, error) {
	if code == "" || version == "" {
		return domain.DataFormat{}, fail("INVALID_ARGUMENT", "格式代码与版本不能为空")
	}
	s.st.Lock()
	defer s.st.Unlock()
	if _, err := s.requireDraftStandard(standardID); err != nil {
		return domain.DataFormat{}, err
	}
	key := standardID + "|" + code
	if _, exists := s.st.Formats[key]; exists {
		return domain.DataFormat{}, fail("CONFLICT", "格式 %s 在该标准版本下已登记", code)
	}
	format := domain.DataFormat{ID: newID("FMT"), StandardID: standardID, Code: code, Version: version, Description: description}
	s.st.Formats[key] = format
	return format, s.st.Save()
}

// RegisterDevice 登记测定设备及其适用范围。
func (s *Service) RegisterDevice(serialNo, model, orgRef string, speciesCodes, traitCodes []string) (domain.Device, error) {
	if serialNo == "" || orgRef == "" {
		return domain.Device{}, fail("INVALID_ARGUMENT", "设备序号与所属机构不能为空")
	}
	if len(speciesCodes) == 0 || len(traitCodes) == 0 {
		return domain.Device{}, fail("INVALID_ARGUMENT", "设备适用范围不能为空")
	}
	s.st.Lock()
	defer s.st.Unlock()
	if _, exists := s.st.Devices[serialNo]; exists {
		return domain.Device{}, fail("CONFLICT", "设备序号 %s 已登记", serialNo)
	}
	device := domain.Device{
		ID:             newID("DEV"),
		SerialNo:       serialNo,
		Model:          model,
		OrgRef:         orgRef,
		SpeciesCodes:   append([]string(nil), speciesCodes...),
		TraitCodes:     append([]string(nil), traitCodes...),
		RegisteredFrom: s.now(),
	}
	s.st.Devices[serialNo] = device
	return device, s.st.Save()
}

// RegisterCalibration 为设备登记校准证书。
func (s *Service) RegisterCalibration(serialNo, certNo, issuedBy string, validFrom, validUntil time.Time, digest string) (domain.CalibrationCertificate, error) {
	if certNo == "" {
		return domain.CalibrationCertificate{}, fail("INVALID_ARGUMENT", "证书编号不能为空")
	}
	if !validUntil.After(validFrom) {
		return domain.CalibrationCertificate{}, fail("INVALID_ARGUMENT", "证书有效期不合法")
	}
	s.st.Lock()
	defer s.st.Unlock()
	device, ok := s.st.Devices[serialNo]
	if !ok {
		return domain.CalibrationCertificate{}, fail("NOT_FOUND", "设备序号 %s 未登记", serialNo)
	}
	for _, cert := range s.st.Calibrations {
		if cert.CertNo == certNo {
			return domain.CalibrationCertificate{}, fail("CONFLICT", "证书编号 %s 已登记", certNo)
		}
	}
	cert := domain.CalibrationCertificate{
		ID:         newID("CAL"),
		DeviceID:   device.ID,
		CertNo:     certNo,
		IssuedBy:   issuedBy,
		ValidFrom:  validFrom,
		ValidUntil: validUntil,
		Digest:     digest,
	}
	s.st.Calibrations[cert.ID] = cert
	return cert, s.st.Save()
}

// RegisterSubject 登记个体及其谱系引用。
func (s *Service) RegisterSubject(ref, speciesCode, orgRef, birthDate string, sireRef, damRef string, generation int) (domain.Subject, error) {
	if ref == "" || speciesCode == "" {
		return domain.Subject{}, fail("INVALID_ARGUMENT", "个体编号与物种代码不能为空")
	}
	if _, err := domain.AgeInDays(birthDate, s.now()); err != nil {
		return domain.Subject{}, fail("INVALID_ARGUMENT", "%s", err.Error())
	}
	s.st.Lock()
	defer s.st.Unlock()
	if _, exists := s.st.Subjects[ref]; exists {
		return domain.Subject{}, fail("CONFLICT", "个体编号 %s 已登记", ref)
	}
	subject := domain.Subject{
		Ref:         ref,
		SpeciesCode: speciesCode,
		OrgRef:      orgRef,
		BirthDate:   birthDate,
		SireRef:     sireRef,
		DamRef:      damRef,
		Generation:  generation,
	}
	s.st.Subjects[ref] = subject
	return subject, s.st.Save()
}
