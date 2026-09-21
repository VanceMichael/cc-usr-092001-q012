// Package provenance 生成并校验育种分析数据集的溯源清单。
//
// 清单回答"这个数据集是怎么来的"：每条记录对应哪个不可变原始读数
// （含载荷摘要）、采集时绑定的哪一版规则（规则哈希）、以及哪张校准证书
// （证书摘要）。清单整体再取一次摘要，任何一处替换都可被重算发现。
package provenance

import (
	"time"

	"example.com/batch-092001-q012/internal/apperr"
	"example.com/batch-092001-q012/internal/canon"
	"example.com/batch-092001-q012/internal/domain"
	"example.com/batch-092001-q012/internal/store"
)

// Service 是溯源清单服务。
type Service struct {
	store *store.Store
	now   func() time.Time
}

// New 创建服务。
func New(s *store.Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: s, now: now}
}

// BuildInput 生成清单入参。
type BuildInput struct {
	CreatorOrg  string
	Description string
	ReadingIDs  []string
	SliceReqID  string
}

// Build 为一组本机构已接收读数构造清单。重复的读数编号只保留一次。
// 跨机构数据集只能由已批准切片的导出流程经 BuildForSlice 生成。
func (svc *Service) Build(in BuildInput) (*domain.DatasetManifest, error) {
	return svc.build(in, false)
}

// BuildForSlice 供切片导出使用：读数来源可跨机构，但必须携带切片申请编号，
// 调用方负责保证读数集合已经过用途范围过滤。
func (svc *Service) BuildForSlice(in BuildInput) (*domain.DatasetManifest, error) {
	if in.SliceReqID == "" {
		return nil, apperr.New(apperr.Validation, "切片数据集清单必须关联切片申请编号")
	}
	return svc.build(in, true)
}

func (svc *Service) build(in BuildInput, viaSlice bool) (*domain.DatasetManifest, error) {
	if in.CreatorOrg == "" {
		return nil, apperr.New(apperr.Unauthorized, "缺少创建机构身份")
	}
	if len(in.ReadingIDs) == 0 {
		return nil, apperr.New(apperr.Validation, "数据集至少包含一条读数")
	}
	if !viaSlice && in.SliceReqID != "" {
		return nil, apperr.New(apperr.Validation, "手工清单不得冒用切片申请编号")
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(in.ReadingIDs))
	for _, id := range in.ReadingIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	m := &domain.DatasetManifest{
		ID:          store.NewID("MAN"),
		SliceReqID:  in.SliceReqID,
		CreatorOrg:  in.CreatorOrg,
		CreatedAt:   svc.now(),
		Description: in.Description,
		ReadingIDs:  ids,
	}
	err := svc.store.Update(func() error {
		for _, id := range ids {
			r, ok := svc.store.GetReading(id)
			if !ok {
				return apperr.New(apperr.NotFound, "读数 %s 不存在，无法入清单", id)
			}
			if r.Status != domain.RecordAccepted {
				return apperr.New(apperr.Validation, "读数 %s 状态为 %s，不能进入分析数据集", id, r.Status)
			}
			if !viaSlice && r.OrgID != in.CreatorOrg {
				return apperr.New(apperr.Forbidden,
					"读数 %s 属于机构 %s，跨机构数据请通过经批准的切片获取", id, r.OrgID)
			}
			certDigest := ""
			if c, ok := svc.store.GetCalibration(r.CalibrationID); ok {
				certDigest = c.CertDigest
			}
			m.Entries = append(m.Entries, domain.ManifestEntry{
				ReadingID:     r.ID,
				RawDigest:     r.PayloadDigest,
				RulesHash:     r.RulesHash,
				StandardID:    r.StandardID,
				StandardVer:   r.StandardVer,
				CalibrationID: r.CalibrationID,
				CertDigest:    certDigest,
				DeviceSerial:  r.DeviceSerial,
				OrgID:         r.OrgID,
				ValueText:     svc.store.EffectiveValueText(r),
			})
		}
		digest, err := canon.Hash(m.Entries)
		if err != nil {
			return apperr.New(apperr.Validation, "清单摘要计算失败: %v", err)
		}
		m.Digest = digest
		svc.store.PutManifest(m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

// EntryMismatch 描述一处核对失败。
type EntryMismatch struct {
	ReadingID string `json:"reading_id"`
	Problem   string `json:"problem"`
}

// VerifyResult 是重算核对结果。
type VerifyResult struct {
	ManifestID     string          `json:"manifest_id"`
	Intact         bool            `json:"intact"`
	DigestMatches  bool            `json:"digest_matches"`
	Mismatches     []EntryMismatch `json:"mismatches,omitempty"`
	CheckedEntries int             `json:"checked_entries"`
}

// Verify 重算清单摘要并逐条比对当前存储中的原始读数、规则哈希与证书摘要。
// 读取权限与 Get 一致。
func (svc *Service) Verify(manifestID, actorOrgID string) (*VerifyResult, error) {
	var m *domain.DatasetManifest
	if err := svc.store.View(func() error {
		found, ok := svc.store.GetManifest(manifestID)
		if !ok {
			return apperr.New(apperr.NotFound, "清单 %s 不存在", manifestID)
		}
		if found.CreatorOrg != actorOrgID {
			if found.SliceReqID == "" {
				return apperr.New(apperr.Forbidden, "清单 %s 不属于机构 %s", manifestID, actorOrgID)
			}
			req, ok := svc.store.GetSlice(found.SliceReqID)
			if !ok || req.OrgID != actorOrgID {
				return apperr.New(apperr.Forbidden, "清单 %s 不属于机构 %s", manifestID, actorOrgID)
			}
		}
		m = found
		return nil
	}); err != nil {
		return nil, err
	}

	res := &VerifyResult{ManifestID: manifestID, Intact: true}
	rebuilt, err := canon.Hash(m.Entries)
	if err != nil {
		return nil, apperr.New(apperr.Validation, "清单摘要重算失败: %v", err)
	}
	res.DigestMatches = rebuilt == m.Digest
	if !res.DigestMatches {
		res.Intact = false
	}

	_ = svc.store.View(func() error {
		for _, e := range m.Entries {
			res.CheckedEntries++
			r, ok := svc.store.GetReading(e.ReadingID)
			if !ok {
				res.Intact = false
				res.Mismatches = append(res.Mismatches, EntryMismatch{e.ReadingID, "原始读数已不存在"})
				continue
			}
			if r.PayloadDigest != e.RawDigest {
				res.Intact = false
				res.Mismatches = append(res.Mismatches, EntryMismatch{e.ReadingID, "原始载荷摘要与清单不一致"})
			}
			if r.RulesHash != e.RulesHash {
				res.Intact = false
				res.Mismatches = append(res.Mismatches, EntryMismatch{e.ReadingID, "读数绑定的规则哈希与清单不一致"})
			}
			if std, ok := svc.store.StandardByHash(e.RulesHash); !ok {
				res.Intact = false
				res.Mismatches = append(res.Mismatches, EntryMismatch{e.ReadingID, "规则哈希无对应登记版本"})
			} else if std.StandardID != e.StandardID || std.Version != e.StandardVer {
				res.Intact = false
				res.Mismatches = append(res.Mismatches, EntryMismatch{e.ReadingID, "规则哈希解析出的标准版本与清单标注不符"})
			}
			if c, ok := svc.store.GetCalibration(e.CalibrationID); ok && c.CertDigest != e.CertDigest {
				res.Intact = false
				res.Mismatches = append(res.Mismatches, EntryMismatch{e.ReadingID, "校准证书摘要与清单不一致"})
			}
			if svc.store.EffectiveValueText(r) != e.ValueText {
				res.Mismatches = append(res.Mismatches, EntryMismatch{e.ReadingID,
					"当前生效值与清单生成时不同（存在后续修订，原始值仍可核对）"})
			}
		}
		return nil
	})
	return res, nil
}

// Get 取清单。手工清单仅创建机构可读；切片派生清单对切片申请机构开放。
func (svc *Service) Get(id, actorOrgID string) (*domain.DatasetManifest, error) {
	var out *domain.DatasetManifest
	err := svc.store.View(func() error {
		m, ok := svc.store.GetManifest(id)
		if !ok {
			return apperr.New(apperr.NotFound, "清单 %s 不存在", id)
		}
		if m.CreatorOrg != actorOrgID {
			if m.SliceReqID == "" {
				return apperr.New(apperr.Forbidden, "清单 %s 不属于机构 %s", id, actorOrgID)
			}
			req, ok := svc.store.GetSlice(m.SliceReqID)
			if !ok || req.OrgID != actorOrgID {
				return apperr.New(apperr.Forbidden, "清单 %s 不属于机构 %s", id, actorOrgID)
			}
		}
		out = m
		return nil
	})
	return out, err
}
