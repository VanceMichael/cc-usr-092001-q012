// Package domain 定义网关的稳定事实：标准版本、设备与校准、个体谱系、
// 原始读数、修订值、质量标记、数据切片与溯源清单。
//
// 设计约束（对应业务规则）：
//   - 标准版本内容冻结后不可修改，采集记录绑定规则哈希；
//   - 原始读数一经接收不可改写，修订与质量标记是独立对象；
//   - 个体合并只新增"合并指向"和谱系边重写，不删除历史标识；
//   - 所有跨机构引用使用稳定编号，不保存真实身份信息。
package domain

import (
	"time"

	"example.com/batch-092001-q012/internal/canon"
)

// 状态词汇。
const (
	StandardActive     = "active"  // 已登记，可在生效时间之后被采集引用
	StandardRetired    = "retired" // 被联合体停用，仅历史记录可引用
	CalibrationValid   = "valid"   // 校准证书在有效期内
	CalibrationDue     = "due"     // 超过建议复校时间但未过最终失效期
	CalibrationDead    = "expired" // 超出失效期，禁止采集
	CalibrationRevoked = "revoked" // 证书被吊销，禁止采集

	RecordAccepted  = "accepted"
	RecordRejected  = "rejected"
	RecordDuplicate = "duplicate" // 幂等命中：同设备同序号且摘要一致

	QualityOpen     = "open"
	QualityResolved = "resolved"
	QualityRejected = "rejected" // 反馈经核查不成立

	SlicePending  = "pending"
	SliceApproved = "approved"
	SliceDenied   = "denied"
	SliceRevoked  = "revoked"
	SliceExpired  = "expired"

	MergeEvent   = "merged"
	CorrectEvent = "identifier_corrected"
)

// Org 是参与联合体的科研院所或养殖企业。
type Org struct {
	ID           string    `json:"org_id"`
	Name         string    `json:"name"`
	Role         string    `json:"role"` // "institute" 或 "farm"
	Active       bool      `json:"active"`
	RegisteredAt time.Time `json:"registered_at"`
}

// TraitDef 是某物种的性状定义。
type TraitDef struct {
	Code       string        `json:"code"`    // 标准内稳定编码，如 "body_weight"
	Species    string        `json:"species"` // "poultry" | "swine"
	Name       string        `json:"name"`
	Unit       string        `json:"unit"`     // 规范单位，如 "kg"
	Decimals   int           `json:"decimals"` // 允许的最大小数位数
	MinValue   canon.Decimal `json:"min_value"`
	MaxValue   canon.Decimal `json:"max_value"`
	MinAgeDays int           `json:"min_age_days"` // 测定时间窗口（按个体出生日）
	MaxAgeDays int           `json:"max_age_days"`
}

// ProtocolStep 描述采集流程的一个步骤。
type ProtocolStep struct {
	Order       int    `json:"order"`
	Action      string `json:"action"`
	Requirement string `json:"requirement"`
}

// FormatRule 描述数据格式约定（字段、类型、编码）。
type FormatRule struct {
	Field    string `json:"field"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Pattern  string `json:"pattern,omitempty"`
}

// RuleSet 是一次标准版本冻结的完整规则正文。
type RuleSet struct {
	Species   []string       `json:"species"`
	Traits    []TraitDef     `json:"traits"`
	Protocols []ProtocolStep `json:"protocols"`
	Formats   []FormatRule   `json:"formats"`
	// AllowedDeviceKinds 限定可用于本标准的设备类型，如 "platform_scale"。
	AllowedDeviceKinds []string `json:"allowed_device_kinds"`
	// MaxClockSkew 设备采集时间相对服务器的最大时钟偏差（分钟）。
	MaxClockSkewMinutes int `json:"max_clock_skew_minutes"`
	// MaxBatchLagHours 离线补传允许距发生时间的最长小时数，0 表示不限制。
	MaxBatchLagHours int `json:"max_batch_lag_hours"`
}

// StandardVersion 是可生效的标准版本。版本号在同一标准内唯一，
// 内容哈希在登记时固化；旧数据始终通过哈希找回当时规则。
type StandardVersion struct {
	StandardID   string    `json:"standard_id"`
	Version      string    `json:"version"`
	EffectiveAt  time.Time `json:"effective_at"`
	RegisteredBy string    `json:"registered_by"`
	RegisteredAt time.Time `json:"registered_at"`
	Status       string    `json:"status"`
	Rules        RuleSet   `json:"rules"`
	RulesHash    string    `json:"rules_hash"`
}

// Device 是测定设备登记。适用范围限定到物种、性状与设备类型。
type Device struct {
	Serial       string    `json:"serial"`
	Kind         string    `json:"kind"`
	VendorModel  string    `json:"vendor_model"`
	OwnerOrgID   string    `json:"owner_org_id"`
	Species      []string  `json:"species_scope"`
	TraitCodes   []string  `json:"trait_scope"`
	RegisteredAt time.Time `json:"registered_at"`
	Active       bool      `json:"active"`
}

// Calibration 是设备校准证书。有效期判定决定采集是否合法。
type Calibration struct {
	ID           string     `json:"calibration_id"`
	DeviceSerial string     `json:"device_serial"`
	IssuedAt     time.Time  `json:"issued_at"`
	DueAt        time.Time  `json:"due_at"`     // 建议复校时间
	ExpiresAt    time.Time  `json:"expires_at"` // 最终失效时间
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	RevokeReason string     `json:"revoke_reason,omitempty"`
	CertRef      string     `json:"cert_ref"`    // 受控引用编号
	CertDigest   string     `json:"cert_digest"` // 证书文件 sha256
	IssuedBy     string     `json:"issued_by"`
	// MaxUncertainty 证书声明的最大不确定度（按性状单位给出上限）。
	MaxUncertainty map[string]canon.Decimal `json:"max_uncertainty"`
}

// Alias 是机构本地编号到规范个体编号的映射。
type Alias struct {
	OrgID       string    `json:"org_id"`
	LocalID     string    `json:"local_id"`
	CanonicalID string    `json:"canonical_id"`
	BoundAt     time.Time `json:"bound_at"`
}

// Animal 是个体。合并目标用 MergedInto 指向存续个体，形成墓碑而不删除。
type Animal struct {
	ID         string     `json:"animal_id"`
	Species    string     `json:"species"`
	Dob        *time.Time `json:"dob,omitempty"`
	SireID     string     `json:"sire_id,omitempty"`
	DamID      string     `json:"dam_id,omitempty"`
	Generation int        `json:"generation"`
	Sex        string     `json:"sex"`
	MergedInto string     `json:"merged_into,omitempty"`
	Active     bool       `json:"active"`
	CreatedAt  time.Time  `json:"created_at"`
}

// LineageEvent 记录谱系边的建立与纠错，保证亲缘链可审计。
type LineageEvent struct {
	ID       string    `json:"lineage_event_id"`
	AnimalID string    `json:"animal_id"`
	Kind     string    `json:"kind"` // "link" | "correction"
	OldSire  string    `json:"old_sire,omitempty"`
	NewSire  string    `json:"new_sire,omitempty"`
	OldDam   string    `json:"old_dam,omitempty"`
	NewDam   string    `json:"new_dam,omitempty"`
	Reason   string    `json:"reason"`
	ByOrgID  string    `json:"by_org_id"`
	At       time.Time `json:"at"`
}

// IdentityEvent 记录合并与标识纠错。
type IdentityEvent struct {
	ID       string    `json:"identity_event_id"`
	Kind     string    `json:"kind"` // MergeEvent | CorrectEvent
	FromID   string    `json:"from_id"`
	ToID     string    `json:"to_id"`
	OldLabel string    `json:"old_label,omitempty"`
	NewLabel string    `json:"new_label,omitempty"`
	Reason   string    `json:"reason"`
	ByOrgID  string    `json:"by_org_id"`
	At       time.Time `json:"at"`
}

// Reading 是不可变的原始读数。即便被拒收，原始载荷也保留。
type Reading struct {
	ID             string    `json:"reading_id"`
	DeviceSerial   string    `json:"device_serial"`
	SourceSequence int64     `json:"source_sequence"`
	AnimalRef      string    `json:"animal_ref"` // 提交时使用的编号（别名/规范号均可）
	AnimalID       string    `json:"animal_id"`  // 解析后的规范个体
	Species        string    `json:"species"`
	TraitCode      string    `json:"trait_code"`
	ValueText      string    `json:"value_text"`
	Unit           string    `json:"unit"`
	Precision      int       `json:"precision"`
	Uncertainty    string    `json:"uncertainty,omitempty"`
	OccurredAt     time.Time `json:"occurred_at"`
	ReceivedAt     time.Time `json:"received_at"`
	StandardID     string    `json:"standard_id"`
	StandardVer    string    `json:"standard_version"`
	RulesHash      string    `json:"rules_hash"`
	CalibrationID  string    `json:"calibration_id"`
	ProtocolRef    string    `json:"protocol_ref"`
	PayloadDigest  string    `json:"payload_digest"`
	RawPayload     string    `json:"raw_payload"`
	Status         string    `json:"status"`
	OrgID          string    `json:"org_id"`
}

// Revision 是对读数的修订。修订不覆盖原值，而是并列的新版本。
type Revision struct {
	ID         string    `json:"revision_id"`
	ReadingID  string    `json:"revision_of"`
	NewValue   string    `json:"new_value_text"`
	Reason     string    `json:"reason"`
	ByOrgID    string    `json:"by_org_id"`
	CreatedAt  time.Time `json:"created_at"`
	Supersedes string    `json:"supersedes,omitempty"` // 上一修订 ID
}

// QualityFlag 是质量标记。质量标记独立于原值与修订存在。
type QualityFlag struct {
	ID         string    `json:"flag_id"`
	ReadingID  string    `json:"reading_id"`
	Severity   string    `json:"severity"` // "info" | "warning" | "error"
	Code       string    `json:"code"`
	Detail     string    `json:"detail"`
	ByOrgID    string    `json:"by_org_id"`
	CreatedAt  time.Time `json:"created_at"`
	Status     string    `json:"status"` // QualityOpen/Resolved/Rejected
	Resolution string    `json:"resolution,omitempty"`
}

// Rejection 记录批量上传中逐条拒收原因，跨机构可见以形成质量反馈闭环。
// 即使被拒收，原始载荷也保留，便于设备方核对与申诉。
type Rejection struct {
	ID             string    `json:"rejection_id"`
	BatchID        string    `json:"batch_id"`
	DeviceSerial   string    `json:"device_serial"`
	SourceSequence int64     `json:"source_sequence"`
	PayloadDigest  string    `json:"payload_digest"`
	RawPayload     string    `json:"raw_payload"`
	OrgID          string    `json:"org_id"`
	Reasons        []string  `json:"reasons"`
	OccurredAt     time.Time `json:"occurred_at"`
	RejectedAt     time.Time `json:"rejected_at"`
	AckedByOrg     bool      `json:"acked_by_org"`
}

// Batch 汇总一次上传（含离线补传）的处理结果。
type Batch struct {
	ID           string    `json:"batch_id"`
	OrgID        string    `json:"org_id"`
	DeviceSerial string    `json:"device_serial"`
	ReceivedAt   time.Time `json:"received_at"`
	Total        int       `json:"total"`
	Accepted     int       `json:"accepted"`
	Duplicated   int       `json:"duplicated"`
	Rejected     int       `json:"rejected"`
}

// SliceRequest 是研究机构的数据切片申请。
type SliceRequest struct {
	ID             string     `json:"slice_request_id"`
	OrgID          string     `json:"org_id"` // 申请机构
	Purpose        string     `json:"purpose"`
	Species        []string   `json:"species"`
	TraitCodes     []string   `json:"trait_codes"`
	DateFrom       time.Time  `json:"date_from"`
	DateTo         time.Time  `json:"date_to"`
	IncludeLineage bool       `json:"include_lineage"`
	Status         string     `json:"status"`
	ReviewedBy     string     `json:"reviewed_by,omitempty"`
	ReviewedAt     *time.Time `json:"reviewed_at,omitempty"`
	DecisionNote   string     `json:"decision_note,omitempty"`
	ExpiresAt      time.Time  `json:"expires_at"`
	CreatedAt      time.Time  `json:"created_at"`
	// ApprovedSubjects 审批时显式圈定的可导出个体范围；为空则按条件+用途机构可见性过滤。
	ApprovedSubjects []string `json:"approved_subjects,omitempty"`
}

// SliceExport 是已生成的切片导出，含假名映射与清单摘要。
type SliceExport struct {
	RequestID   string             `json:"slice_request_id"`
	GeneratedAt time.Time          `json:"generated_at"`
	ManifestID  string             `json:"manifest_id"`
	Records     []SliceRecord      `json:"records"`
	Lineage     []SliceLineageEdge `json:"lineage,omitempty"`
	Subjects    []SliceSubject     `json:"subjects"`
}

// SliceSubject 是切片内个体的假名化条目；不含真实身份信息。
type SliceSubject struct {
	Pseudonym  string `json:"pseudonym"`
	Species    string `json:"species"`
	Generation int    `json:"generation"`
	DobMonth   string `json:"dob_month,omitempty"` // 精度裁剪到月
	Sex        string `json:"sex,omitempty"`
}

// SliceRecord 是切片内一条测定记录。
type SliceRecord struct {
	Pseudonym     string    `json:"pseudonym"`
	TraitCode     string    `json:"trait_code"`
	ValueText     string    `json:"value_text"` // 最新生效值（原始或最新修订）
	Unit          string    `json:"unit"`
	OccurredAt    time.Time `json:"occurred_at"`
	ReadingID     string    `json:"reading_id"`
	RulesHash     string    `json:"rules_hash"`
	CalibrationID string    `json:"calibration_id"`
}

// SliceLineageEdge 是经过裁剪的亲缘边，仅保留假名。
type SliceLineageEdge struct {
	ChildPseudonym string `json:"child_pseudonym"`
	SirePseudonym  string `json:"sire_pseudonym,omitempty"`
	DamPseudonym   string `json:"dam_pseudonym,omitempty"`
}

// DatasetManifest 是育种分析数据集的溯源清单，
// 可证明该数据集由哪些原始采集、校准证书与规则版本构成。
type DatasetManifest struct {
	ID          string          `json:"manifest_id"`
	SliceReqID  string          `json:"slice_request_id,omitempty"`
	CreatorOrg  string          `json:"creator_org"`
	CreatedAt   time.Time       `json:"created_at"`
	Description string          `json:"description"`
	ReadingIDs  []string        `json:"reading_ids"`
	Entries     []ManifestEntry `json:"entries"`
	Digest      string          `json:"digest"`
}

// ManifestEntry 是清单中一条读数的完整溯源三元组。
type ManifestEntry struct {
	ReadingID     string `json:"reading_id"`
	RawDigest     string `json:"raw_payload_digest"`
	RulesHash     string `json:"rules_hash"`
	StandardID    string `json:"standard_id"`
	StandardVer   string `json:"standard_version"`
	CalibrationID string `json:"calibration_id"`
	CertDigest    string `json:"cert_digest"`
	DeviceSerial  string `json:"device_serial"`
	OrgID         string `json:"org_id"`
	ValueText     string `json:"value_text"`
}
