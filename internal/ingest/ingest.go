// Package ingest 实现采集上传的校验管道与离线补传去重。
//
// 处理顺序（逐条）：
//  1. 幂等去重：同一设备序号只认第一次成功接收的摘要；
//  2. 载荷摘要核对：提交的 payload_digest 必须等于规范哈希；
//  3. 引用解析：设备、个体（支持机构本地号）、生效标准版本、性状定义、校准证书；
//  4. 规则校验：单位、精度、量值范围、日龄窗口、时钟偏差、补传时限、设备适用范围、不确定度；
//  5. 分流落库：通过的存为不可变原始读数；失败的保留原始载荷与全部拒收原因。
//
// 原始读数（Reading）、修订值（Revision）与质量标记（QualityFlag）分表保存，
// 任何后续修订或质量判定都不覆盖原始值。
//
// 锁约定：引用解析全部在写事务之外以只读方式完成；写事务内只调用 store 的
// 无锁映射方法做去重判定与落库，避免 RWMutex 重入死锁。
package ingest

import (
	"encoding/json"
	"strings"
	"time"

	"example.com/batch-092001-q012/internal/apperr"
	"example.com/batch-092001-q012/internal/canon"
	"example.com/batch-092001-q012/internal/devices"
	"example.com/batch-092001-q012/internal/domain"
	"example.com/batch-092001-q012/internal/standards"
	"example.com/batch-092001-q012/internal/store"
	"example.com/batch-092001-q012/internal/subjects"
)

// GatewayOrgID 是网关自动标记（如临期校准告警）的系统主体。
const GatewayOrgID = "GATEWAY"

// Service 是采集管道服务。
type Service struct {
	store     *store.Store
	standards *standards.Service
	devices   *devices.Service
	subjects  *subjects.Service
	now       func() time.Time
}

// New 创建采集服务。
func New(s *store.Store, std *standards.Service, dev *devices.Service, sub *subjects.Service, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: s, standards: std, devices: dev, subjects: sub, now: now}
}

// ItemInput 是单条采集记录的交换结构。
type ItemInput struct {
	SourceSequence int64     `json:"source_sequence"`
	AnimalRef      string    `json:"animal_ref"`
	Species        string    `json:"species"`
	TraitCode      string    `json:"trait_code"`
	Value          string    `json:"value"`
	Unit           string    `json:"unit"`
	Uncertainty    string    `json:"uncertainty,omitempty"`
	OccurredAt     time.Time `json:"occurred_at"`
	ProtocolRef    string    `json:"protocol_ref,omitempty"`
	PayloadDigest  string    `json:"payload_digest"`
}

// BatchInput 是一次上传（在线或离线补传）的信封。
type BatchInput struct {
	StandardID   string            `json:"standard_id"`
	DeviceSerial string            `json:"device_serial"`
	Items        []json.RawMessage `json:"items"`
}

// ItemResult 是逐条处理结果。
type ItemResult struct {
	SourceSequence int64    `json:"source_sequence"`
	Status         string   `json:"status"`
	ReadingID      string   `json:"reading_id,omitempty"`
	RejectionID    string   `json:"rejection_id,omitempty"`
	Reasons        []string `json:"reasons,omitempty"`
}

// BatchResult 是批次处理结果汇总。
type BatchResult struct {
	Batch *domain.Batch `json:"batch"`
	Items []ItemResult  `json:"items"`
}

// resolved 汇集事务外解析到的引用。
type resolved struct {
	animal    *domain.Animal
	standard  *domain.StandardVersion
	trait     domain.TraitDef
	hasTrait  bool
	calib     *domain.Calibration
	calStatus string
	parsed    canon.Decimal
}

// Ingest 处理一整个批次。单条失败不影响其他记录；批次整体总是返回结果，
// 只有信封级错误（机构/设备/标准不存在、空批次）才返回 error。
func (svc *Service) Ingest(orgID string, in BatchInput) (*BatchResult, error) {
	if orgID == "" {
		return nil, apperr.New(apperr.Unauthorized, "缺少上传机构身份")
	}
	if in.StandardID == "" || in.DeviceSerial == "" {
		return nil, apperr.New(apperr.Validation, "standard_id 与 device_serial 为必填项")
	}
	if len(in.Items) == 0 {
		return nil, apperr.New(apperr.Validation, "批次不能为空")
	}
	if err := svc.store.View(func() error {
		org, ok := svc.store.GetOrg(orgID)
		if !ok || !org.Active {
			return apperr.New(apperr.Unauthorized, "机构 %s 未登记或已停用", orgID)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	device, err := svc.devices.GetDevice(in.DeviceSerial)
	if err != nil {
		return nil, err
	}
	if !device.Active {
		return nil, apperr.New(apperr.Validation, "设备 %s 已停用", in.DeviceSerial)
	}
	// 只有设备登记的归属机构可以用它上传：离线补传发生在设备回收数据后，
	// 由属主机构统一接入；非属主机构拿他方设备序列号不能向系统写入。
	if device.OwnerOrgID != orgID {
		return nil, apperr.New(apperr.Forbidden,
			"设备 %s 登记归属机构 %s，机构 %s 无权用其上传", device.Serial, device.OwnerOrgID, orgID)
	}

	batch := &domain.Batch{
		ID:           store.NewID("BAT"),
		OrgID:        orgID,
		DeviceSerial: in.DeviceSerial,
		ReceivedAt:   svc.now(),
		Total:        len(in.Items),
	}
	results := make([]ItemResult, 0, len(in.Items))
	for _, raw := range in.Items {
		results = append(results, svc.processItem(batch.ID, orgID, in.StandardID, device, raw))
	}
	for _, r := range results {
		switch r.Status {
		case domain.RecordAccepted:
			batch.Accepted++
		case domain.RecordDuplicate:
			batch.Duplicated++
		case domain.RecordRejected:
			batch.Rejected++
		}
	}
	if err := svc.store.Update(func() error { svc.store.PutBatch(batch); return nil }); err != nil {
		return nil, err
	}
	return &BatchResult{Batch: batch, Items: results}, nil
}

// processItem 完成单条处理：结构校验 → 摘要核对 → 事务外解析与规则校验
// → 单个写事务内原子地完成去重判定与分流落库。
func (svc *Service) processItem(batchID, orgID, standardID string, device *domain.Device, raw json.RawMessage) ItemResult {
	res := ItemResult{}

	var item ItemInput
	structural := []string{}
	if err := json.Unmarshal(raw, &item); err != nil {
		structural = append(structural, "记录无法解析为 JSON: "+err.Error())
	}
	res.SourceSequence = item.SourceSequence
	if item.SourceSequence <= 0 {
		structural = append(structural, "source_sequence 必须为正整数")
	}
	if item.OccurredAt.IsZero() {
		structural = append(structural, "occurred_at 缺失或非法（需带偏移量的 ISO 8601）")
	}

	actualDigest, digestErr := digestWithoutField(raw, "payload_digest")
	if digestErr != nil {
		structural = append(structural, "载荷摘要计算失败: "+digestErr.Error())
	}
	if item.PayloadDigest == "" {
		structural = append(structural, "缺少 payload_digest")
	} else if actualDigest != "" && item.PayloadDigest != actualDigest {
		structural = append(structural,
			apperr.DigestMismatch+": 提交摘要 "+item.PayloadDigest+" 与载荷实际摘要 "+actualDigest+" 不一致")
	}

	if len(structural) > 0 {
		svc.persistRejection(batchID, orgID, device.Serial, item, raw, structural, &res)
		return res
	}

	// ---- 事务外：引用解析（只读） ----
	rc := resolved{}
	reasons := []string{}

	animal, err := svc.subjects.Resolve(orgID, item.AnimalRef)
	if err != nil {
		reasons = append(reasons, apperr.UnknownSubject+": "+err.Error())
	} else {
		rc.animal = animal
		if animal.Species != item.Species {
			reasons = append(reasons, apperr.Validation+": 个体物种 "+animal.Species+" 与申报物种 "+item.Species+" 不一致")
		}
	}

	std, err := svc.standards.EffectiveAt(standardID, item.OccurredAt)
	if err != nil {
		reasons = append(reasons, err.Error())
	} else {
		rc.standard = std
		if t, found := standards.Trait(std.Rules, item.Species, item.TraitCode); found {
			rc.trait, rc.hasTrait = t, true
		} else {
			reasons = append(reasons, apperr.Validation+": 性状 "+item.Species+"/"+item.TraitCode+
				" 不在标准 "+standardID+"@"+std.Version+" 中")
		}
		kindOK := len(std.Rules.AllowedDeviceKinds) == 0
		for _, k := range std.Rules.AllowedDeviceKinds {
			if k == device.Kind {
				kindOK = true
				break
			}
		}
		if !kindOK {
			reasons = append(reasons, apperr.OutOfScope+": 设备类型 "+device.Kind+" 不在标准允许范围内")
		}
		reasons = append(reasons, svc.checkWindows(std.Rules, item)...)
	}

	if !devices.InScope(device, item.Species, item.TraitCode) {
		reasons = append(reasons, apperr.OutOfScope+
			": 设备 "+device.Serial+" 未声明适用 "+item.Species+"/"+item.TraitCode)
	}

	cal, calStatus, calErr := svc.devices.CalibrationAt(device.Serial, item.OccurredAt)
	if calErr != nil {
		reasons = append(reasons, calErr.Error())
	} else {
		rc.calib, rc.calStatus = cal, calStatus
		switch calStatus {
		case domain.CalibrationDead:
			reasons = append(reasons, apperr.CalibrationBad+": 校准证书 "+cal.ID+" 已于 "+cal.ExpiresAt.Format(time.RFC3339)+" 失效")
		case domain.CalibrationRevoked:
			reasons = append(reasons, apperr.CalibrationBad+": 校准证书 "+cal.ID+" 已被吊销")
		}
	}

	if rc.hasTrait {
		if strings.TrimSpace(item.Unit) != rc.trait.Unit {
			reasons = append(reasons, apperr.UnitMismatch+": 申报单位 "+item.Unit+"，规范单位 "+rc.trait.Unit+"（网关不做隐式换算）")
		}
		d, valReasons := validateValue(item, rc.trait)
		rc.parsed = d
		reasons = append(reasons, valReasons...)
		if item.Uncertainty != "" {
			reasons = append(reasons, validateUncertainty(item, rc.trait, cal)...)
		}
		if rc.animal != nil && (rc.trait.MinAgeDays != 0 || rc.trait.MaxAgeDays != 0) {
			if rc.animal.Dob == nil {
				reasons = append(reasons, apperr.WindowMissed+": 个体缺少出生日期，无法核对测定日龄窗口")
			} else {
				ageDays := int(item.OccurredAt.Sub(*rc.animal.Dob).Hours() / 24)
				if rc.trait.MinAgeDays != 0 && ageDays < rc.trait.MinAgeDays {
					reasons = append(reasons, apperr.WindowMissed+": 测定日龄 "+itoa(ageDays)+" 早于下限 "+itoa(rc.trait.MinAgeDays))
				}
				if rc.trait.MaxAgeDays != 0 && ageDays > rc.trait.MaxAgeDays {
					reasons = append(reasons, apperr.WindowMissed+": 测定日龄 "+itoa(ageDays)+" 晚于上限 "+itoa(rc.trait.MaxAgeDays))
				}
			}
		}
	}

	// ---- 写事务：去重判定 + 分流落库（仅用无锁 store 方法） ----
	_ = svc.store.Update(func() error {
		if existing, ok := svc.store.ReadingByDedup(device.Serial, item.SourceSequence); ok {
			if existing.PayloadDigest == actualDigest {
				res.Status = domain.RecordDuplicate
				res.ReadingID = existing.ID
				res.Reasons = []string{"设备序号 " + itoa(int(item.SourceSequence)) + " 已接收（幂等去重）"}
				return nil
			}
			svc.persistRejectionLocked(batchID, orgID, device, item, raw,
				[]string{apperr.DigestMismatch + ": 同设备同序号已存在不同摘要的记录，既有 reading=" + existing.ID}, &res)
			return nil
		}
		if len(reasons) > 0 {
			svc.persistRejectionLocked(batchID, orgID, device, item, raw, reasons, &res)
			return nil
		}

		reading := &domain.Reading{
			ID:             store.NewID("READ"),
			DeviceSerial:   device.Serial,
			SourceSequence: item.SourceSequence,
			AnimalRef:      item.AnimalRef,
			AnimalID:       rc.animal.ID,
			Species:        item.Species,
			TraitCode:      item.TraitCode,
			ValueText:      rc.parsed.String(),
			Unit:           item.Unit,
			Precision:      decimalPlaces(item.Value),
			Uncertainty:    item.Uncertainty,
			OccurredAt:     item.OccurredAt,
			ReceivedAt:     svc.now(),
			StandardID:     rc.standard.StandardID,
			StandardVer:    rc.standard.Version,
			RulesHash:      rc.standard.RulesHash,
			CalibrationID:  rc.calib.ID,
			ProtocolRef:    item.ProtocolRef,
			PayloadDigest:  actualDigest,
			RawPayload:     string(raw),
			Status:         domain.RecordAccepted,
			OrgID:          orgID,
		}
		svc.store.PutReading(reading)
		res.Status = domain.RecordAccepted
		res.ReadingID = reading.ID

		if rc.calStatus == domain.CalibrationDue {
			svc.store.AppendFlag(&domain.QualityFlag{
				ID:        store.NewID("FLAG"),
				ReadingID: reading.ID,
				Severity:  "warning",
				Code:      "calibration_due",
				Detail:    "校准证书 " + rc.calib.ID + " 已过建议复校期 " + rc.calib.DueAt.Format(time.RFC3339) + "，请尽快安排复校",
				ByOrgID:   GatewayOrgID,
				CreatedAt: svc.now(),
				Status:    domain.QualityOpen,
			})
		}
		return nil
	})
	return res
}

// checkWindows 校验时钟偏差与离线补传时限。
func (svc *Service) checkWindows(rules domain.RuleSet, item ItemInput) []string {
	reasons := []string{}
	now := svc.now()
	if rules.MaxClockSkewMinutes > 0 {
		skew := item.OccurredAt.Sub(now)
		if skew > time.Duration(rules.MaxClockSkewMinutes)*time.Minute {
			reasons = append(reasons, apperr.ClockSkew+": 采集时间超前服务器 "+
				skew.Truncate(time.Second).String()+"，超过允许偏差 "+itoa(rules.MaxClockSkewMinutes)+" 分钟")
		}
	}
	if rules.MaxBatchLagHours > 0 {
		lag := now.Sub(item.OccurredAt)
		if lag > time.Duration(rules.MaxBatchLagHours)*time.Hour {
			reasons = append(reasons, apperr.BatchLag+": 离线补传滞后 "+
				lag.Truncate(time.Hour).String()+"，超过时限 "+itoa(rules.MaxBatchLagHours)+" 小时")
		}
	}
	return reasons
}

// validateValue 校验十进制可解析、精度位数与量值范围。
func validateValue(item ItemInput, trait domain.TraitDef) (canon.Decimal, []string) {
	reasons := []string{}
	d, err := canon.ParseDecimal(item.Value)
	if err != nil {
		reasons = append(reasons, apperr.Validation+": 量值必须为十进制字符串: "+err.Error())
		return canon.Decimal{}, reasons
	}
	if places := decimalPlaces(item.Value); places > trait.Decimals {
		reasons = append(reasons, apperr.PrecisionBad+": 量值小数位数 "+itoa(places)+
			" 超过性状允许精度 "+itoa(trait.Decimals)+" 位")
	}
	if !trait.MinValue.IsZero() && d.Cmp(trait.MinValue) < 0 {
		reasons = append(reasons, apperr.ValueOutOfRange+": 量值 "+item.Value+" 低于下限 "+trait.MinValue.String())
	}
	if !trait.MaxValue.IsZero() && d.Cmp(trait.MaxValue) > 0 {
		reasons = append(reasons, apperr.ValueOutOfRange+": 量值 "+item.Value+" 高于上限 "+trait.MaxValue.String())
	}
	return d, reasons
}

// validateUncertainty 核对申报不确定度不超过校准证书声明的性状上限。
func validateUncertainty(item ItemInput, trait domain.TraitDef, cal *domain.Calibration) []string {
	u, err := canon.ParseDecimal(item.Uncertainty)
	if err != nil {
		return []string{apperr.Validation + ": 不确定度必须为十进制字符串: " + err.Error()}
	}
	if cal == nil {
		return nil
	}
	limit, ok := cal.MaxUncertainty[item.TraitCode]
	if !ok {
		return []string{apperr.UncertaintyBad + ": 校准证书 " + cal.ID + " 未声明性状 " + item.TraitCode + " 的不确定度上限"}
	}
	if u.Cmp(limit) > 0 {
		return []string{apperr.UncertaintyBad + ": 申报不确定度 " + item.Uncertainty +
			trait.Unit + " 超过证书上限 " + limit.String() + trait.Unit}
	}
	return nil
}

// persistRejection 在独立事务中保存拒收（结构性失败路径）。
func (svc *Service) persistRejection(batchID, orgID, serial string, item ItemInput, raw json.RawMessage, reasons []string, res *ItemResult) {
	_ = svc.store.Update(func() error {
		svc.persistRejectionLocked(batchID, orgID, &domain.Device{Serial: serial}, item, raw, reasons, res)
		return nil
	})
}

// persistRejectionLocked 保存拒收记录并回填结果，必须在写事务内调用。
func (svc *Service) persistRejectionLocked(batchID, orgID string, device *domain.Device, item ItemInput, raw json.RawMessage, reasons []string, res *ItemResult) {
	r := &domain.Rejection{
		ID:             store.NewID("REJ"),
		BatchID:        batchID,
		DeviceSerial:   device.Serial,
		SourceSequence: item.SourceSequence,
		PayloadDigest:  item.PayloadDigest,
		RawPayload:     string(raw),
		OrgID:          orgID,
		Reasons:        reasons,
		OccurredAt:     item.OccurredAt,
		RejectedAt:     svc.now(),
	}
	svc.store.PutRejection(r)
	res.Status = domain.RecordRejected
	res.RejectionID = r.ID
	res.Reasons = reasons
}

// digestWithoutField 计算删除指定字段后的规范哈希。
func digestWithoutField(raw json.RawMessage, field string) (string, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", err
	}
	delete(m, field)
	return canon.Hash(m)
}

// decimalPlaces 统计十进制文本的小数位数（先去空白，支持负号）。
func decimalPlaces(text string) int {
	text = strings.TrimSpace(text)
	if i := strings.IndexByte(text, '.'); i >= 0 {
		return len(text) - i - 1
	}
	return 0
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [24]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
