package service

import (
	"strconv"
	"time"

	"example.com/batch-092001-q012/internal/domain"
)

// UploadItem 是批量上传中的一条采集记录。
type UploadItem struct {
	SubjectRef     string `json:"subject_ref"`
	TraitCode      string `json:"trait_code"`
	Value          string `json:"value"` // 原始读数原文，用于精度校验
	Unit           string `json:"unit"`
	OccurredAt     string `json:"occurred_at"`
	SourceSequence int64  `json:"source_sequence"`
}

// ItemResult 是单条记录的处理结果。
type ItemResult struct {
	Index         int      `json:"index"`
	Status        string   `json:"status"` // accepted / rejected / duplicate
	ObservationID string   `json:"observation_id,omitempty"`
	DuplicateOf   string   `json:"duplicate_of,omitempty"`
	RejectReasons []string `json:"reject_reasons,omitempty"`
}

// BatchResult 汇总一次批量上传。
type BatchResult struct {
	BatchID    string       `json:"batch_id"`
	Accepted   int          `json:"accepted"`
	Rejected   int          `json:"rejected"`
	Duplicated int          `json:"duplicated"`
	Items      []ItemResult `json:"items"`
}

// IngestBatch 接收一批采集记录：按发生时刻的标准版本校验单位、精度、
// 时间窗口与设备适用范围，并按（设备序号, 来源序号）去重。
// 被拒记录同样保存并附拒收原因，供跨机构质量反馈核对。
func (s *Service) IngestBatch(orgRef, deviceSerial, format string, items []UploadItem) (BatchResult, error) {
	if orgRef == "" || deviceSerial == "" || format == "" {
		return BatchResult{}, fail("INVALID_ARGUMENT", "机构、设备序号与数据格式不能为空")
	}
	if len(items) == 0 {
		return BatchResult{}, fail("INVALID_ARGUMENT", "上传内容不能为空")
	}
	s.st.Lock()
	defer s.st.Unlock()

	device, ok := s.st.Devices[deviceSerial]
	if !ok {
		return BatchResult{}, fail("UNKNOWN_DEVICE", "设备序号 %s 未登记", deviceSerial)
	}
	if device.OrgRef != orgRef {
		return BatchResult{}, fail("DEVICE_ORG_MISMATCH", "设备 %s 不属于机构 %s", deviceSerial, orgRef)
	}

	batch := domain.UploadBatch{
		ID:           newID("BAT"),
		OrgRef:       orgRef,
		DeviceSerial: deviceSerial,
		Format:       format,
		ItemCount:    len(items),
		ReceivedAt:   s.now(),
	}
	result := BatchResult{BatchID: batch.ID}

	for index, item := range items {
		observation, itemResult := s.ingestOne(batch, device, format, index, item)
		s.st.Observations[observation.ID] = observation
		if observation.Status == domain.ObsAccepted {
			s.st.Dedup[deviceSerial+"|"+strconv.FormatInt(item.SourceSequence, 10)] = observation.ID
			result.Accepted++
		} else if itemResult.Status == "duplicate" {
			result.Duplicated++
		} else {
			result.Rejected++
		}
		result.Items = append(result.Items, itemResult)
	}

	s.st.Batches[batch.ID] = batch
	if err := s.st.Save(); err != nil {
		return BatchResult{}, err
	}
	return result, nil
}

// ingestOne 校验并构造单条观测记录。调用方须已持有锁。
func (s *Service) ingestOne(batch domain.UploadBatch, device domain.Device, format string, index int, item UploadItem) (domain.Observation, ItemResult) {
	observation := domain.Observation{
		ID:             newID("OBS"),
		BatchID:        batch.ID,
		SubjectRef:     item.SubjectRef,
		TraitCode:      item.TraitCode,
		RawValueText:   item.Value,
		Unit:           item.Unit,
		DeviceSerial:   device.SerialNo,
		SourceSequence: item.SourceSequence,
		Status:         domain.ObsRejected,
		ReceivedAt:     batch.ReceivedAt,
	}
	itemResult := ItemResult{Index: index, ObservationID: observation.ID}

	// 离线补传去重：同一设备序号与来源序号只接受一次（仅对已被接受的记录生效，
	// 被拒记录修正后可用同一来源序号重新上报）。
	if existingID, seen := s.st.Dedup[device.SerialNo+"|"+strconv.FormatInt(item.SourceSequence, 10)]; seen {
		observation.Status = domain.ObsRejected
		observation.RejectReasons = []string{"DUPLICATE"}
		itemResult.Status = "duplicate"
		itemResult.DuplicateOf = existingID
		return observation, itemResult
	}

	var reasons []string

	value, valueErr := strconv.ParseFloat(item.Value, 64)
	if valueErr != nil {
		reasons = append(reasons, "INVALID_VALUE")
	} else {
		observation.RawValue = value
	}

	occurredAt, timeErr := domain.ParseTime(item.OccurredAt)
	if timeErr != nil {
		reasons = append(reasons, "INVALID_OCCURRED_AT")
	} else {
		observation.OccurredAt = occurredAt
		if occurredAt.After(s.now()) {
			reasons = append(reasons, "FUTURE_OCCURRED_AT")
		}
	}

	// 个体与谱系：合并过的旧标识解析到规范个体，亲缘链不中断。
	canonicalRef, subject, subjectOK := s.resolveSubject(item.SubjectRef)
	if !subjectOK {
		reasons = append(reasons, "UNKNOWN_SUBJECT")
	} else {
		observation.CanonicalRef = canonicalRef
	}

	// 发生时刻适用的标准版本：标准升级只影响生效后的采集。
	var standard domain.StandardVersion
	var speciesCode string
	if subjectOK {
		speciesCode = subject.SpeciesCode
	}
	if timeErr == nil {
		resolved, found := s.ResolveStandard(occurredAt)
		if !found {
			reasons = append(reasons, "NO_EFFECTIVE_STANDARD")
		} else {
			standard = resolved
			observation.StandardID = standard.ID
		}
	}

	var trait domain.TraitDefinition
	traitOK := false
	if observation.StandardID != "" && subjectOK {
		if _, ok := s.st.Species[standard.ID+"|"+speciesCode]; !ok {
			reasons = append(reasons, "UNKNOWN_SPECIES")
		}
		found, ok := s.st.Traits[standard.ID+"|"+speciesCode+"|"+item.TraitCode]
		if !ok {
			reasons = append(reasons, "UNKNOWN_TRAIT")
		} else {
			trait = found
			traitOK = true
		}
		if _, ok := s.st.Formats[standard.ID+"|"+format]; !ok {
			reasons = append(reasons, "UNKNOWN_FORMAT")
		}
	}

	if traitOK {
		if item.Unit != trait.Unit {
			reasons = append(reasons, "UNIT_MISMATCH")
		}
		if places, ok := domain.DecimalPlaces(item.Value); ok && places > trait.Precision {
			reasons = append(reasons, "PRECISION_EXCEEDED")
		}
		if valueErr == nil {
			if trait.MinValue != nil && value < *trait.MinValue {
				reasons = append(reasons, "VALUE_OUT_OF_RANGE")
			}
			if trait.MaxValue != nil && value > *trait.MaxValue {
				reasons = append(reasons, "VALUE_OUT_OF_RANGE")
			}
		}
		// 采集流程的日龄时间窗口
		if protocol, ok := s.st.Protocols[standard.ID+"|"+speciesCode+"|"+item.TraitCode]; ok && timeErr == nil {
			if age, err := domain.AgeInDays(subject.BirthDate, occurredAt); err == nil {
				if age < protocol.MinAgeDays || age > protocol.MaxAgeDays {
					reasons = append(reasons, "AGE_OUT_OF_WINDOW")
				}
			}
		}
		// 设备适用范围：物种与性状都须在设备登记范围内
		if !contains(device.SpeciesCodes, speciesCode) || !contains(device.TraitCodes, item.TraitCode) {
			reasons = append(reasons, "DEVICE_SCOPE_MISMATCH")
		}
	}

	// 校准证书须覆盖采集发生时刻
	if timeErr == nil {
		cert, ok := s.validCalibration(device.ID, occurredAt)
		if !ok {
			reasons = append(reasons, "NO_VALID_CALIBRATION")
		} else {
			observation.CalibrationID = cert.ID
		}
	}

	if len(reasons) == 0 {
		observation.Status = domain.ObsAccepted
		itemResult.Status = "accepted"
	} else {
		observation.RejectReasons = reasons
		itemResult.Status = "rejected"
		itemResult.RejectReasons = reasons
	}
	return observation, itemResult
}

// validCalibration 返回覆盖 occurredAt 的校准证书。
func (s *Service) validCalibration(deviceID string, occurredAt time.Time) (domain.CalibrationCertificate, bool) {
	for _, cert := range s.st.Calibrations {
		if cert.DeviceID != deviceID {
			continue
		}
		if !occurredAt.Before(cert.ValidFrom) && !occurredAt.After(cert.ValidUntil) {
			return cert, true
		}
	}
	return domain.CalibrationCertificate{}, false
}

func contains(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

// GetObservation 返回观测及其修订与质量标记（分别保存、合并展示）。
func (s *Service) GetObservation(id string) (domain.Observation, []domain.Revision, []domain.QualityMark, error) {
	s.st.Lock()
	defer s.st.Unlock()
	observation, ok := s.st.Observations[id]
	if !ok {
		return domain.Observation{}, nil, nil, fail("NOT_FOUND", "观测 %s 不存在", id)
	}
	return observation, s.st.Revisions[id], s.st.QualityMarks[id], nil
}

// AddRevision 为已接受的观测追加修订值；原始读数保持不变。
func (s *Service) AddRevision(observationID, revisedText, reason, revisedBy string) (domain.Revision, error) {
	if reason == "" || revisedBy == "" {
		return domain.Revision{}, fail("INVALID_ARGUMENT", "修订原因与修订人不能为空")
	}
	value, err := strconv.ParseFloat(revisedText, 64)
	if err != nil {
		return domain.Revision{}, fail("INVALID_VALUE", "修订值 %q 不是合法数值", revisedText)
	}
	s.st.Lock()
	defer s.st.Unlock()
	observation, ok := s.st.Observations[observationID]
	if !ok {
		return domain.Revision{}, fail("NOT_FOUND", "观测 %s 不存在", observationID)
	}
	if observation.Status != domain.ObsAccepted {
		return domain.Revision{}, fail("CONFLICT", "被拒收的观测不能修订")
	}
	revision := domain.Revision{
		ID:            newID("REV"),
		ObservationID: observationID,
		RevisedText:   revisedText,
		RevisedValue:  value,
		Reason:        reason,
		RevisedBy:     revisedBy,
		RevisedAt:     s.now(),
	}
	s.st.Revisions[observationID] = append(s.st.Revisions[observationID], revision)
	return revision, s.st.Save()
}

// AddQualityMark 为观测追加质量标记，与读数分别保存。
func (s *Service) AddQualityMark(observationID, flag, note, markedBy string) (domain.QualityMark, error) {
	if flag == "" || markedBy == "" {
		return domain.QualityMark{}, fail("INVALID_ARGUMENT", "质量标记与标记人不能为空")
	}
	s.st.Lock()
	defer s.st.Unlock()
	if _, ok := s.st.Observations[observationID]; !ok {
		return domain.QualityMark{}, fail("NOT_FOUND", "观测 %s 不存在", observationID)
	}
	mark := domain.QualityMark{
		ID:            newID("QM"),
		ObservationID: observationID,
		Flag:          flag,
		Note:          note,
		MarkedBy:      markedBy,
		MarkedAt:      s.now(),
	}
	s.st.QualityMarks[observationID] = append(s.st.QualityMarks[observationID], mark)
	return mark, s.st.Save()
}

// CurrentValue 返回观测的当前值：最新修订值，无修订时为原始读数。
func CurrentValue(observation domain.Observation, revisions []domain.Revision) string {
	if len(revisions) == 0 {
		return observation.RawValueText
	}
	latest := revisions[0]
	for _, revision := range revisions[1:] {
		if revision.RevisedAt.After(latest.RevisedAt) {
			latest = revision
		}
	}
	return latest.RevisedText
}
