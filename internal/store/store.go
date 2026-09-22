// Package store 提供基于本地 JSON 文件的持久化。
// 每类事实一个文件，写入时整体落盘；本地数据目录不得提交到仓库。
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"example.com/batch-092001-q012/internal/domain"
)

// Store 集中保存各类业务事实，并负责落盘。
type Store struct {
	dir string
	mu  sync.Mutex

	Standards    map[string]domain.StandardVersion        // 键：标准版本 ID
	Species      map[string]domain.Species                // 键：标准ID|物种代码
	Traits       map[string]domain.TraitDefinition        // 键：标准ID|物种|性状代码
	Protocols    map[string]domain.CollectionProtocol     // 键：标准ID|物种|性状代码
	Formats      map[string]domain.DataFormat             // 键：标准ID|格式代码
	Devices      map[string]domain.Device                 // 键：设备序号
	Calibrations map[string]domain.CalibrationCertificate // 键：证书 ID
	Subjects     map[string]domain.Subject                // 键：个体引用编号
	Merges       map[string]domain.SubjectMerge           // 键：被合并的引用编号
	Batches      map[string]domain.UploadBatch            // 键：批次 ID
	Observations map[string]domain.Observation            // 键：观测 ID
	Revisions    map[string][]domain.Revision             // 键：观测 ID
	QualityMarks map[string][]domain.QualityMark          // 键：观测 ID
	Feedback     map[string][]domain.QualityFeedback      // 键：观测 ID
	Slices       map[string]domain.SliceRequest           // 键：申请 ID
	Datasets     map[string]domain.AnalysisDataset        // 键：数据集 ID
	Dedup        map[string]string                        // 键：设备序号|来源序号 → 观测 ID
}

// Open 打开（必要时创建）本地数据目录并载入已有事实。
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("数据目录不能为空")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	s := &Store{dir: dir}
	s.Standards = map[string]domain.StandardVersion{}
	s.Species = map[string]domain.Species{}
	s.Traits = map[string]domain.TraitDefinition{}
	s.Protocols = map[string]domain.CollectionProtocol{}
	s.Formats = map[string]domain.DataFormat{}
	s.Devices = map[string]domain.Device{}
	s.Calibrations = map[string]domain.CalibrationCertificate{}
	s.Subjects = map[string]domain.Subject{}
	s.Merges = map[string]domain.SubjectMerge{}
	s.Batches = map[string]domain.UploadBatch{}
	s.Observations = map[string]domain.Observation{}
	s.Revisions = map[string][]domain.Revision{}
	s.QualityMarks = map[string][]domain.QualityMark{}
	s.Feedback = map[string][]domain.QualityFeedback{}
	s.Slices = map[string]domain.SliceRequest{}
	s.Datasets = map[string]domain.AnalysisDataset{}
	s.Dedup = map[string]string{}
	load := func(name string, target any) error {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("读取 %s 失败: %w", name, err)
		}
		if err := json.Unmarshal(data, target); err != nil {
			return fmt.Errorf("解析 %s 失败: %w", name, err)
		}
		return nil
	}
	if err := load("standards.json", &s.Standards); err != nil {
		return nil, err
	}
	if err := load("species.json", &s.Species); err != nil {
		return nil, err
	}
	if err := load("traits.json", &s.Traits); err != nil {
		return nil, err
	}
	if err := load("protocols.json", &s.Protocols); err != nil {
		return nil, err
	}
	if err := load("formats.json", &s.Formats); err != nil {
		return nil, err
	}
	if err := load("devices.json", &s.Devices); err != nil {
		return nil, err
	}
	if err := load("calibrations.json", &s.Calibrations); err != nil {
		return nil, err
	}
	if err := load("subjects.json", &s.Subjects); err != nil {
		return nil, err
	}
	if err := load("merges.json", &s.Merges); err != nil {
		return nil, err
	}
	if err := load("batches.json", &s.Batches); err != nil {
		return nil, err
	}
	if err := load("observations.json", &s.Observations); err != nil {
		return nil, err
	}
	if err := load("revisions.json", &s.Revisions); err != nil {
		return nil, err
	}
	if err := load("quality_marks.json", &s.QualityMarks); err != nil {
		return nil, err
	}
	if err := load("feedback.json", &s.Feedback); err != nil {
		return nil, err
	}
	if err := load("slices.json", &s.Slices); err != nil {
		return nil, err
	}
	if err := load("datasets.json", &s.Datasets); err != nil {
		return nil, err
	}
	if err := load("dedup.json", &s.Dedup); err != nil {
		return nil, err
	}
	return s, nil
}

// Lock 在变更与落盘期间持有互斥锁。
func (s *Store) Lock() { s.mu.Lock() }

// Unlock 释放互斥锁。
func (s *Store) Unlock() { s.mu.Unlock() }

// Save 把全部事实整体落盘。调用方须已持有锁。
func (s *Store) Save() error {
	dump := func(name string, value any) error {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return fmt.Errorf("编码 %s 失败: %w", name, err)
		}
		path := filepath.Join(s.dir, name)
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return fmt.Errorf("写入 %s 失败: %w", name, err)
		}
		if err := os.Rename(tmp, path); err != nil {
			return fmt.Errorf("替换 %s 失败: %w", name, err)
		}
		return nil
	}
	steps := []struct {
		name  string
		value any
	}{
		{"standards.json", s.Standards},
		{"species.json", s.Species},
		{"traits.json", s.Traits},
		{"protocols.json", s.Protocols},
		{"formats.json", s.Formats},
		{"devices.json", s.Devices},
		{"calibrations.json", s.Calibrations},
		{"subjects.json", s.Subjects},
		{"merges.json", s.Merges},
		{"batches.json", s.Batches},
		{"observations.json", s.Observations},
		{"revisions.json", s.Revisions},
		{"quality_marks.json", s.QualityMarks},
		{"feedback.json", s.Feedback},
		{"slices.json", s.Slices},
		{"datasets.json", s.Datasets},
		{"dedup.json", s.Dedup},
	}
	for _, step := range steps {
		if err := dump(step.name, step.value); err != nil {
			return err
		}
	}
	return nil
}
