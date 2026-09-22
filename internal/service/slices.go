package service

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"example.com/batch-092001-q012/internal/domain"
)

// RequestSlice 研究机构提交数据切片申请，初始为待审批。
func (s *Service) RequestSlice(orgRef, purpose, speciesCode string, traitCodes []string, from, to time.Time, includeSubject bool) (domain.SliceRequest, error) {
	if orgRef == "" || purpose == "" || speciesCode == "" || len(traitCodes) == 0 {
		return domain.SliceRequest{}, fail("INVALID_ARGUMENT", "机构、用途、物种与性状范围不能为空")
	}
	if !to.After(from) {
		return domain.SliceRequest{}, fail("INVALID_ARGUMENT", "时间范围不合法")
	}
	s.st.Lock()
	defer s.st.Unlock()
	request := domain.SliceRequest{
		ID:             newID("SLC"),
		OrgRef:         orgRef,
		Purpose:        purpose,
		SpeciesCode:    speciesCode,
		TraitCodes:     append([]string(nil), traitCodes...),
		From:           from,
		To:             to,
		IncludeSubject: includeSubject,
		Status:         domain.SlicePending,
	}
	s.st.Slices[request.ID] = request
	return request, s.st.Save()
}

// DecideSlice 审批或驳回切片申请。
func (s *Service) DecideSlice(id string, approve bool, decidedBy string) (domain.SliceRequest, error) {
	if decidedBy == "" {
		return domain.SliceRequest{}, fail("INVALID_ARGUMENT", "审批人不能为空")
	}
	s.st.Lock()
	defer s.st.Unlock()
	request, ok := s.st.Slices[id]
	if !ok {
		return domain.SliceRequest{}, fail("NOT_FOUND", "切片申请 %s 不存在", id)
	}
	if request.Status != domain.SlicePending {
		return domain.SliceRequest{}, fail("CONFLICT", "切片申请 %s 已审批", id)
	}
	now := s.now()
	request.DecidedBy = decidedBy
	request.DecidedAt = &now
	if approve {
		request.Status = domain.SliceApproved
	} else {
		request.Status = domain.SliceRejected
	}
	s.st.Slices[id] = request
	return request, s.st.Save()
}

// SliceRecord 是切片输出中的一条记录。
// 未获主体信息授权时，个体编号替换为申请维度的假名，谱系信息不输出。
type SliceRecord struct {
	ObservationID string    `json:"observation_id"`
	SubjectKey    string    `json:"subject_key"`
	SpeciesCode   string    `json:"species_code"`
	TraitCode     string    `json:"trait_code"`
	Value         string    `json:"value"`
	Unit          string    `json:"unit"`
	OccurredAt    time.Time `json:"occurred_at"`
	QualityFlags  []string  `json:"quality_flags,omitempty"`
	SireKey       string    `json:"sire_key,omitempty"`
	DamKey        string    `json:"dam_key,omitempty"`
}

// SliceData 按批准的切片范围输出数据；范围之外的性状、物种与主体信息不出现。
func (s *Service) SliceData(id string) ([]SliceRecord, error) {
	s.st.Lock()
	defer s.st.Unlock()
	request, ok := s.st.Slices[id]
	if !ok {
		return nil, fail("NOT_FOUND", "切片申请 %s 不存在", id)
	}
	if request.Status != domain.SliceApproved {
		return nil, fail("PERMISSION_DENIED", "切片申请 %s 未获批准", id)
	}
	subjectKey := func(canonicalRef string) string {
		if request.IncludeSubject {
			return canonicalRef
		}
		sum := sha256.Sum256([]byte(request.ID + "|" + canonicalRef))
		return "ANON-" + hex.EncodeToString(sum[:8])
	}
	var records []SliceRecord
	for _, observation := range s.st.Observations {
		if observation.Status != domain.ObsAccepted {
			continue
		}
		if observation.OccurredAt.Before(request.From) || observation.OccurredAt.After(request.To) {
			continue
		}
		if !contains(request.TraitCodes, observation.TraitCode) {
			continue
		}
		_, subject, ok := s.resolveSubject(observation.CanonicalRef)
		if !ok || subject.SpeciesCode != request.SpeciesCode {
			continue
		}
		record := SliceRecord{
			ObservationID: observation.ID,
			SubjectKey:    subjectKey(observation.CanonicalRef),
			SpeciesCode:   subject.SpeciesCode,
			TraitCode:     observation.TraitCode,
			Value:         CurrentValue(observation, s.st.Revisions[observation.ID]),
			Unit:          observation.Unit,
			OccurredAt:    observation.OccurredAt,
		}
		for _, mark := range s.st.QualityMarks[observation.ID] {
			record.QualityFlags = append(record.QualityFlags, mark.Flag)
		}
		if request.IncludeSubject {
			if sire, _, ok := s.resolveSubject(subject.SireRef); ok && subject.SireRef != "" {
				record.SireKey = sire
			}
			if dam, _, ok := s.resolveSubject(subject.DamRef); ok && subject.DamRef != "" {
				record.DamKey = dam
			}
		}
		records = append(records, record)
	}
	return records, nil
}
