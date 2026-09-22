package service

import (
	"example.com/batch-092001-q012/internal/domain"
)

// AddFeedback 登记一条跨机构质量反馈，可针对任何已保存的观测（含被拒记录）。
func (s *Service) AddFeedback(observationID, fromOrg, code, detail string) (domain.QualityFeedback, error) {
	if fromOrg == "" || code == "" {
		return domain.QualityFeedback{}, fail("INVALID_ARGUMENT", "反馈机构与反馈代码不能为空")
	}
	s.st.Lock()
	defer s.st.Unlock()
	if _, ok := s.st.Observations[observationID]; !ok {
		return domain.QualityFeedback{}, fail("NOT_FOUND", "观测 %s 不存在", observationID)
	}
	feedback := domain.QualityFeedback{
		ID:            newID("FB"),
		ObservationID: observationID,
		FromOrg:       fromOrg,
		Code:          code,
		Detail:        detail,
		CreatedAt:     s.now(),
	}
	s.st.Feedback[observationID] = append(s.st.Feedback[observationID], feedback)
	return feedback, s.st.Save()
}

// FeedbackFor 返回某条观测收到的全部质量反馈。
func (s *Service) FeedbackFor(observationID string) ([]domain.QualityFeedback, error) {
	s.st.Lock()
	defer s.st.Unlock()
	if _, ok := s.st.Observations[observationID]; !ok {
		return nil, fail("NOT_FOUND", "观测 %s 不存在", observationID)
	}
	return append([]domain.QualityFeedback(nil), s.st.Feedback[observationID]...), nil
}

// RejectionView 是一条被拒记录及其原因，供机构核对补正。
type RejectionView struct {
	ObservationID string   `json:"observation_id"`
	BatchID       string   `json:"batch_id"`
	SubjectRef    string   `json:"subject_ref"`
	TraitCode     string   `json:"trait_code"`
	DeviceSerial  string   `json:"device_serial"`
	RejectReasons []string `json:"reject_reasons"`
}

// RejectionsForOrg 返回某机构上传批次中被拒收的记录及原因。
func (s *Service) RejectionsForOrg(orgRef string) []RejectionView {
	s.st.Lock()
	defer s.st.Unlock()
	orgBatches := map[string]bool{}
	for _, batch := range s.st.Batches {
		if batch.OrgRef == orgRef {
			orgBatches[batch.ID] = true
		}
	}
	var views []RejectionView
	for _, observation := range s.st.Observations {
		if observation.Status != domain.ObsRejected || !orgBatches[observation.BatchID] {
			continue
		}
		views = append(views, RejectionView{
			ObservationID: observation.ID,
			BatchID:       observation.BatchID,
			SubjectRef:    observation.SubjectRef,
			TraitCode:     observation.TraitCode,
			DeviceSerial:  observation.DeviceSerial,
			RejectReasons: append([]string(nil), observation.RejectReasons...),
		})
	}
	return views
}
