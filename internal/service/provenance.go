package service

import (
	"example.com/batch-092001-q012/internal/domain"
)

// CreateDataset 登记一个育种分析数据集，来源观测必须全部存在且已被接受。
func (s *Service) CreateDataset(name, orgRef string, observationIDs []string) (domain.AnalysisDataset, error) {
	if name == "" || orgRef == "" || len(observationIDs) == 0 {
		return domain.AnalysisDataset{}, fail("INVALID_ARGUMENT", "数据集名称、机构与来源观测不能为空")
	}
	s.st.Lock()
	defer s.st.Unlock()
	for _, id := range observationIDs {
		observation, ok := s.st.Observations[id]
		if !ok {
			return domain.AnalysisDataset{}, fail("NOT_FOUND", "观测 %s 不存在", id)
		}
		if observation.Status != domain.ObsAccepted {
			return domain.AnalysisDataset{}, fail("CONFLICT", "观测 %s 未被接受，不能作为数据集来源", id)
		}
	}
	dataset := domain.AnalysisDataset{
		ID:             newID("DSET"),
		Name:           name,
		OrgRef:         orgRef,
		ObservationIDs: append([]string(nil), observationIDs...),
		CreatedAt:      s.now(),
	}
	s.st.Datasets[dataset.ID] = dataset
	return dataset, s.st.Save()
}

// ProvenanceEntry 证明一条来源观测的原始采集、校准与规则版本。
type ProvenanceEntry struct {
	ObservationID     string   `json:"observation_id"`
	SubjectRef        string   `json:"subject_ref"` // 规范个体编号
	TraitCode         string   `json:"trait_code"`
	OccurredAt        string   `json:"occurred_at"`
	RawValueText      string   `json:"raw_value_text"`
	CurrentValueText  string   `json:"current_value_text"`
	Unit              string   `json:"unit"`
	DeviceSerial      string   `json:"device_serial"`
	DeviceModel       string   `json:"device_model"`
	CalibrationCertNo string   `json:"calibration_cert_no"`
	StandardCode      string   `json:"standard_code"`
	StandardVersion   string   `json:"standard_version"`
	QualityFlags      []string `json:"quality_flags"`
	RevisionCount     int      `json:"revision_count"`
}

// ProvenanceReport 是数据集的完整溯源报告。
type ProvenanceReport struct {
	DatasetID   string            `json:"dataset_id"`
	DatasetName string            `json:"dataset_name"`
	OrgRef      string            `json:"org_ref"`
	Entries     []ProvenanceEntry `json:"entries"`
}

// Provenance 生成数据集的溯源报告：每条来源观测对应的原始读数、
// 测定设备、校准证书与接收时适用的标准版本。
func (s *Service) Provenance(datasetID string) (ProvenanceReport, error) {
	s.st.Lock()
	defer s.st.Unlock()
	dataset, ok := s.st.Datasets[datasetID]
	if !ok {
		return ProvenanceReport{}, fail("NOT_FOUND", "数据集 %s 不存在", datasetID)
	}
	report := ProvenanceReport{
		DatasetID:   dataset.ID,
		DatasetName: dataset.Name,
		OrgRef:      dataset.OrgRef,
	}
	for _, id := range dataset.ObservationIDs {
		observation, ok := s.st.Observations[id]
		if !ok {
			return ProvenanceReport{}, fail("NOT_FOUND", "来源观测 %s 已缺失", id)
		}
		entry := ProvenanceEntry{
			ObservationID:    observation.ID,
			SubjectRef:       observation.CanonicalRef,
			TraitCode:        observation.TraitCode,
			OccurredAt:       observation.OccurredAt.Format("2006-01-02T15:04:05Z07:00"),
			RawValueText:     observation.RawValueText,
			CurrentValueText: CurrentValue(observation, s.st.Revisions[id]),
			Unit:             observation.Unit,
			DeviceSerial:     observation.DeviceSerial,
			RevisionCount:    len(s.st.Revisions[id]),
		}
		if device, ok := s.st.Devices[observation.DeviceSerial]; ok {
			entry.DeviceModel = device.Model
		}
		if cert, ok := s.st.Calibrations[observation.CalibrationID]; ok {
			entry.CalibrationCertNo = cert.CertNo
		}
		if standard, ok := s.st.Standards[observation.StandardID]; ok {
			entry.StandardCode = standard.Code
			entry.StandardVersion = standard.Version
		}
		for _, mark := range s.st.QualityMarks[id] {
			entry.QualityFlags = append(entry.QualityFlags, mark.Flag)
		}
		report.Entries = append(report.Entries, entry)
	}
	return report, nil
}
