// Package domain 定义接入网关保存的稳定业务事实。
// 所有外部主体只使用不含真实身份信息的引用编号，
// 时间一律采用带偏移量的 RFC3339 字符串。
package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// StandardStatus 表示标准版本的生命周期状态。
type StandardStatus string

const (
	StandardDraft     StandardStatus = "draft"
	StandardEffective StandardStatus = "effective"
	StandardRetired   StandardStatus = "retired"
)

// StandardVersion 是一个可生效的标准版本。
// 生效后其下登记的规则不可再修改，升级只能登记新版本。
type StandardVersion struct {
	ID            string         `json:"id"`
	Code          string         `json:"code"`
	Version       string         `json:"version"`
	Status        StandardStatus `json:"status"`
	EffectiveFrom time.Time      `json:"effective_from"`
	RetiredAt     *time.Time     `json:"retired_at,omitempty"`
	RegisteredAt  time.Time      `json:"registered_at"`
}

// Species 登记在某个标准版本下的物种。
type Species struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	StandardID string `json:"standard_id"`
}

// TraitDefinition 是某标准版本下某物种的性状定义。
type TraitDefinition struct {
	ID          string   `json:"id"`
	StandardID  string   `json:"standard_id"`
	SpeciesCode string   `json:"species_code"`
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Unit        string   `json:"unit"`
	Precision   int      `json:"precision"` // 允许的小数位数
	MinValue    *float64 `json:"min_value,omitempty"`
	MaxValue    *float64 `json:"max_value,omitempty"`
}

// CollectionProtocol 规定性状采集的日龄时间窗口。
type CollectionProtocol struct {
	ID          string `json:"id"`
	StandardID  string `json:"standard_id"`
	SpeciesCode string `json:"species_code"`
	TraitCode   string `json:"trait_code"`
	MinAgeDays  int    `json:"min_age_days"`
	MaxAgeDays  int    `json:"max_age_days"`
}

// DataFormat 登记某标准版本接受的上报数据格式。
type DataFormat struct {
	ID          string `json:"id"`
	StandardID  string `json:"standard_id"`
	Code        string `json:"code"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

// Device 是登记的测定设备，以设备序号唯一标识。
type Device struct {
	ID             string    `json:"id"`
	SerialNo       string    `json:"serial_no"`
	Model          string    `json:"model"`
	OrgRef         string    `json:"org_ref"`
	SpeciesCodes   []string  `json:"species_codes"`
	TraitCodes     []string  `json:"trait_codes"`
	RegisteredFrom time.Time `json:"registered_from"`
}

// CalibrationCertificate 是设备的校准证书，带有效期。
type CalibrationCertificate struct {
	ID         string    `json:"id"`
	DeviceID   string    `json:"device_id"`
	CertNo     string    `json:"cert_no"`
	IssuedBy   string    `json:"issued_by"`
	ValidFrom  time.Time `json:"valid_from"`
	ValidUntil time.Time `json:"valid_until"`
	Digest     string    `json:"digest"` // 证书材料的 sha256 摘要
}

// Subject 是登记个体，只保存稳定引用编号与谱系引用。
type Subject struct {
	Ref         string `json:"ref"`
	SpeciesCode string `json:"species_code"`
	OrgRef      string `json:"org_ref"`
	BirthDate   string `json:"birth_date"` // YYYY-MM-DD
	SireRef     string `json:"sire_ref,omitempty"`
	DamRef      string `json:"dam_ref,omitempty"`
	Generation  int    `json:"generation"`
}

// SubjectMerge 记录个体合并或标识纠错：MergedRef 归并到 CanonicalRef。
// 只追加映射、不删除历史，保证亲缘链可追溯。
type SubjectMerge struct {
	MergedRef    string    `json:"merged_ref"`
	CanonicalRef string    `json:"canonical_ref"`
	Reason       string    `json:"reason"`
	MergedAt     time.Time `json:"merged_at"`
}

// ObservationStatus 是观测记录的接收状态。
type ObservationStatus string

const (
	ObsAccepted ObservationStatus = "accepted"
	ObsRejected ObservationStatus = "rejected"
)

// Observation 保存一条原始读数。原始读数一经保存不可改写，
// 修订值与质量标记分别保存在 Revision 与 QualityMark 中。
type Observation struct {
	ID             string            `json:"id"`
	BatchID        string            `json:"batch_id"`
	SubjectRef     string            `json:"subject_ref"`   // 上报时的原始标识
	CanonicalRef   string            `json:"canonical_ref"` // 接收时解析到的规范个体
	TraitCode      string            `json:"trait_code"`
	RawValueText   string            `json:"raw_value_text"` // 原始读数原文
	RawValue       float64           `json:"raw_value"`
	Unit           string            `json:"unit"`
	OccurredAt     time.Time         `json:"occurred_at"`
	DeviceSerial   string            `json:"device_serial"`
	SourceSequence int64             `json:"source_sequence"`
	StandardID     string            `json:"standard_id"`
	CalibrationID  string            `json:"calibration_id,omitempty"`
	Status         ObservationStatus `json:"status"`
	RejectReasons  []string          `json:"reject_reasons,omitempty"`
	ReceivedAt     time.Time         `json:"received_at"`
}

// Revision 是对某条观测的修订值，与原始读数分别保存。
type Revision struct {
	ID            string    `json:"id"`
	ObservationID string    `json:"observation_id"`
	RevisedText   string    `json:"revised_text"`
	RevisedValue  float64   `json:"revised_value"`
	Reason        string    `json:"reason"`
	RevisedBy     string    `json:"revised_by"`
	RevisedAt     time.Time `json:"revised_at"`
}

// QualityMark 是挂在观测上的质量标记，与读数分别保存。
type QualityMark struct {
	ID            string    `json:"id"`
	ObservationID string    `json:"observation_id"`
	Flag          string    `json:"flag"`
	Note          string    `json:"note,omitempty"`
	MarkedBy      string    `json:"marked_by"`
	MarkedAt      time.Time `json:"marked_at"`
}

// QualityFeedback 是跨机构质量反馈。
type QualityFeedback struct {
	ID            string    `json:"id"`
	ObservationID string    `json:"observation_id"`
	FromOrg       string    `json:"from_org"`
	Code          string    `json:"code"`
	Detail        string    `json:"detail,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// UploadBatch 记录一次批量上传（含离线设备补传）。
type UploadBatch struct {
	ID           string    `json:"id"`
	OrgRef       string    `json:"org_ref"`
	DeviceSerial string    `json:"device_serial"`
	Format       string    `json:"format"`
	ItemCount    int       `json:"item_count"`
	ReceivedAt   time.Time `json:"received_at"`
}

// SliceStatus 是数据切片申请的状态。
type SliceStatus string

const (
	SlicePending  SliceStatus = "pending"
	SliceApproved SliceStatus = "approved"
	SliceRejected SliceStatus = "rejected"
)

// SliceRequest 是研究机构的数据切片申请。
// IncludeSubject 为 false 时，输出中不出现可识别的主体信息。
type SliceRequest struct {
	ID             string      `json:"id"`
	OrgRef         string      `json:"org_ref"`
	Purpose        string      `json:"purpose"`
	SpeciesCode    string      `json:"species_code"`
	TraitCodes     []string    `json:"trait_codes"`
	From           time.Time   `json:"from"`
	To             time.Time   `json:"to"`
	IncludeSubject bool        `json:"include_subject"`
	Status         SliceStatus `json:"status"`
	DecidedBy      string      `json:"decided_by,omitempty"`
	DecidedAt      *time.Time  `json:"decided_at,omitempty"`
}

// AnalysisDataset 登记一个育种分析数据集及其来源观测。
type AnalysisDataset struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	OrgRef         string    `json:"org_ref"`
	ObservationIDs []string  `json:"observation_ids"`
	CreatedAt      time.Time `json:"created_at"`
}

// ParseTime 解析带偏移量的 RFC3339 时间。
func ParseTime(text string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}, fmt.Errorf("时间 %q 不是带偏移量的 ISO 8601 格式", text)
	}
	return parsed, nil
}

// DecimalPlaces 返回数值文本的小数位数；文本不是合法数值时返回 false。
func DecimalPlaces(text string) (int, bool) {
	trimmed := strings.TrimSpace(text)
	if _, err := strconv.ParseFloat(trimmed, 64); err != nil {
		return 0, false
	}
	if strings.ContainsAny(trimmed, "eE") {
		// 科学计数法无法稳定还原原始精度，按原文拆分
		parts := strings.FieldsFunc(trimmed, func(r rune) bool { return r == 'e' || r == 'E' })
		trimmed = parts[0]
	}
	dot := strings.Index(trimmed, ".")
	if dot < 0 {
		return 0, true
	}
	return len(trimmed) - dot - 1, true
}

// AgeInDays 计算 occurredAt 相对出生日期（YYYY-MM-DD）的日龄。
func AgeInDays(birthDate string, occurredAt time.Time) (int, error) {
	birth, err := time.Parse("2006-01-02", birthDate)
	if err != nil {
		return 0, fmt.Errorf("出生日期 %q 不是 YYYY-MM-DD 格式", birthDate)
	}
	return int(occurredAt.Sub(birth).Hours() / 24), nil
}
