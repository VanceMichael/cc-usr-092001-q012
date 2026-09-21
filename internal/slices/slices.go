// Package slices 管理研究机构数据切片的申请、审批与用途受限导出。
//
// 最小授权原则在导出环节强制：
//   - 只有 approved 且未过期的申请可导出；
//   - 记录严格限制在申请并经批准的物种、性状、时间窗与主体名单内；
//   - 导出物不含机构身份、机构本地编号、真实标识：个体替换为按申请派生的假名，
//     出生日期精度裁剪到月，亲缘边仅保留假名且只保留切片内可见的节点；
//   - 用途（purpose）与审批决定随申请留痕，越出用途范围的主体信息根本不会进入导出。
package slices

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"

	"example.com/batch-092001-q012/internal/apperr"
	"example.com/batch-092001-q012/internal/domain"
	"example.com/batch-092001-q012/internal/store"
)

// Service 是数据切片服务。
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

// ApplyInput 切片申请入参。
type ApplyInput struct {
	OrgID          string
	Purpose        string
	Species        []string
	TraitCodes     []string
	DateFrom       time.Time
	DateTo         time.Time
	IncludeLineage bool
	ValidDays      int
}

// Apply 由研究机构提交申请。
func (svc *Service) Apply(in ApplyInput) (*domain.SliceRequest, error) {
	if in.OrgID == "" || in.Purpose == "" {
		return nil, apperr.New(apperr.Validation, "申请机构与用途说明不能为空")
	}
	if err := svc.store.View(func() error {
		org, ok := svc.store.GetOrg(in.OrgID)
		if !ok || !org.Active {
			return apperr.New(apperr.Unauthorized, "机构 %s 未登记或已停用", in.OrgID)
		}
		if org.Role != "institute" {
			return apperr.New(apperr.Forbidden, "仅研究机构可申请数据切片（机构 %s 角色为 %s）", in.OrgID, org.Role)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if len(in.Species) == 0 || len(in.TraitCodes) == 0 {
		return nil, apperr.New(apperr.Validation, "申请必须限定物种与性状范围")
	}
	if in.DateFrom.IsZero() || in.DateTo.IsZero() || !in.DateTo.After(in.DateFrom) {
		return nil, apperr.New(apperr.Validation, "申请时间窗非法")
	}
	validDays := in.ValidDays
	if validDays <= 0 {
		validDays = 90
	}
	req := &domain.SliceRequest{
		ID:             store.NewID("SLICE"),
		OrgID:          in.OrgID,
		Purpose:        in.Purpose,
		Species:        append([]string(nil), in.Species...),
		TraitCodes:     append([]string(nil), in.TraitCodes...),
		DateFrom:       in.DateFrom,
		DateTo:         in.DateTo,
		IncludeLineage: in.IncludeLineage,
		Status:         domain.SlicePending,
		ExpiresAt:      svc.now().AddDate(0, 0, validDays),
		CreatedAt:      svc.now(),
	}
	if err := svc.store.Update(func() error { svc.store.PutSlice(req); return nil }); err != nil {
		return nil, err
	}
	return req, nil
}

// Review 由联合体审批。approved=false 即拒绝并需说明；approved=true 时
// 可通过 approvedSubjects 把主体范围收窄到显式名单（空名单表示按条件全量）。
// 审批可同时缩短有效期。
func (svc *Service) Review(requestID, reviewerOrg string, approve bool, note string, approvedSubjects []string, expireAt time.Time) (*domain.SliceRequest, error) {
	var out *domain.SliceRequest
	err := svc.store.Update(func() error {
		req, ok := svc.store.GetSlice(requestID)
		if !ok {
			return apperr.New(apperr.NotFound, "切片申请 %s 不存在", requestID)
		}
		if req.Status != domain.SlicePending {
			return apperr.New(apperr.SliceState, "申请 %s 已审批（当前状态 %s）", requestID, req.Status)
		}
		if !approve && note == "" {
			return apperr.New(apperr.Validation, "拒绝申请必须填写理由")
		}
		now := svc.now()
		req.ReviewedBy = reviewerOrg
		req.ReviewedAt = &now
		req.DecisionNote = note
		if approve {
			req.Status = domain.SliceApproved
			req.ApprovedSubjects = append([]string(nil), approvedSubjects...)
			if !expireAt.IsZero() && expireAt.Before(req.ExpiresAt) {
				req.ExpiresAt = expireAt
			}
		} else {
			req.Status = domain.SliceDenied
		}
		out = req
		return nil
	})
	return out, err
}

// Revoke 由联合体撤销已批准申请（如发现用途违规）。
func (svc *Service) Revoke(requestID, reviewerOrg, note string) error {
	return svc.store.Update(func() error {
		req, ok := svc.store.GetSlice(requestID)
		if !ok {
			return apperr.New(apperr.NotFound, "切片申请 %s 不存在", requestID)
		}
		if req.Status != domain.SliceApproved {
			return apperr.New(apperr.SliceState, "仅已批准申请可撤销，当前状态 %s", req.Status)
		}
		req.Status = domain.SliceRevoked
		req.ReviewedBy = reviewerOrg
		req.DecisionNote = note
		return nil
	})
}

// Get 取申请。
func (svc *Service) Get(id string) (*domain.SliceRequest, error) {
	var out *domain.SliceRequest
	err := svc.store.View(func() error {
		r, ok := svc.store.GetSlice(id)
		if !ok {
			return apperr.New(apperr.NotFound, "切片申请 %s 不存在", id)
		}
		out = r
		return nil
	})
	return out, err
}

// Export 为申请机构生成假名化切片。
func (svc *Service) Export(orgID, requestID string) (*domain.SliceExport, error) {
	var export *domain.SliceExport
	err := svc.store.Update(func() error {
		req, ok := svc.store.GetSlice(requestID)
		if !ok {
			return apperr.New(apperr.NotFound, "切片申请 %s 不存在", requestID)
		}
		if req.OrgID != orgID {
			return apperr.New(apperr.Forbidden, "申请 %s 不属于机构 %s", requestID, orgID)
		}
		if req.Status == domain.SliceExpired || (req.Status == domain.SliceApproved && !svc.now().Before(req.ExpiresAt)) {
			req.Status = domain.SliceExpired
			return apperr.New(apperr.SliceState, "切片申请 %s 已过期", requestID)
		}
		if req.Status != domain.SliceApproved {
			return apperr.New(apperr.SliceState, "切片申请 %s 当前状态 %s，不可导出", requestID, req.Status)
		}

		speciesSet := toSet(req.Species)
		traitSet := toSet(req.TraitCodes)
		var subjectAllow map[string]bool
		if len(req.ApprovedSubjects) > 0 {
			subjectAllow = toSet(req.ApprovedSubjects)
		}

		// 假名表只为本切片内出现的存续个体建立。
		pseudonymOf := map[string]string{}
		pseudo := func(animalID string) string {
			if p, ok := pseudonymOf[animalID]; ok {
				return p
			}
			// 假名按"申请+个体"派生：跨切片不可链接，切片内稳定可重复生成。
			mac := hmac.New(sha256.New, []byte(req.ID))
			mac.Write([]byte(animalID))
			p := "PSU-" + hex.EncodeToString(mac.Sum(nil))[:12]
			pseudonymOf[animalID] = p
			return p
		}

		export = &domain.SliceExport{
			RequestID:   req.ID,
			GeneratedAt: svc.now(),
			ManifestID:  "", // 由 API 层在导出后用 provenance 生成清单并回填
		}

		for _, r := range svc.store.ListReadings() {
			if r.Status != domain.RecordAccepted {
				continue
			}
			if !speciesSet[r.Species] || !traitSet[r.TraitCode] {
				continue
			}
			if r.OccurredAt.Before(req.DateFrom) || r.OccurredAt.After(req.DateTo) {
				continue
			}
			animal, ok := svc.store.ResolveAnimal(r.AnimalID)
			if !ok {
				continue
			}
			if subjectAllow != nil && !subjectAllow[animal.ID] {
				continue
			}
			export.Records = append(export.Records, domain.SliceRecord{
				Pseudonym:     pseudo(animal.ID),
				TraitCode:     r.TraitCode,
				ValueText:     svc.store.EffectiveValueText(r),
				Unit:          r.Unit,
				OccurredAt:    r.OccurredAt,
				ReadingID:     r.ID,
				RulesHash:     r.RulesHash,
				CalibrationID: r.CalibrationID,
			})
		}
		sort.Slice(export.Records, func(i, j int) bool {
			if export.Records[i].Pseudonym != export.Records[j].Pseudonym {
				return export.Records[i].Pseudonym < export.Records[j].Pseudonym
			}
			return export.Records[i].OccurredAt.Before(export.Records[j].OccurredAt)
		})

		// 主体表：只含切片中出现的个体，出生日期裁剪到月，无任何机构/本地编号。
		subjectIDs := make([]string, 0, len(pseudonymOf))
		for id := range pseudonymOf {
			subjectIDs = append(subjectIDs, id)
		}
		sort.Strings(subjectIDs)
		for _, id := range subjectIDs {
			a, _ := svc.store.GetAnimal(id)
			if a == nil {
				continue
			}
			s := domain.SliceSubject{
				Pseudonym:  pseudonymOf[id],
				Species:    a.Species,
				Generation: a.Generation,
				Sex:        a.Sex,
			}
			if a.Dob != nil {
				s.DobMonth = a.Dob.Format("2006-01")
			}
			export.Subjects = append(export.Subjects, s)
		}

		// 亲缘边：仅当申请批准包含谱系，且边的三端都在切片可见集合内才导出。
		if req.IncludeLineage {
			seenEdge := map[string]bool{}
			for _, id := range subjectIDs {
				a, _ := svc.store.GetAnimal(id)
				if a == nil {
					continue
				}
				edge := domain.SliceLineageEdge{ChildPseudonym: pseudonymOf[a.ID]}
				ok := false
				if a.SireID != "" {
					if sire, found := svc.store.ResolveAnimal(a.SireID); found && pseudonymOf[sire.ID] != "" {
						edge.SirePseudonym = pseudonymOf[sire.ID]
						ok = true
					}
				}
				if a.DamID != "" {
					if dam, found := svc.store.ResolveAnimal(a.DamID); found && pseudonymOf[dam.ID] != "" {
						edge.DamPseudonym = pseudonymOf[dam.ID]
						ok = true
					}
				}
				key := edge.ChildPseudonym + "|" + edge.SirePseudonym + "|" + edge.DamPseudonym
				if ok && !seenEdge[key] {
					seenEdge[key] = true
					export.Lineage = append(export.Lineage, edge)
				}
			}
		}
		return nil
	})
	return export, err
}

// ListByOrg 列出机构自己的申请（在读锁内拷贝，避免外部无锁遍历）。
func (svc *Service) ListByOrg(orgID string) []*domain.SliceRequest {
	out := []*domain.SliceRequest{}
	_ = svc.store.View(func() error {
		for _, r := range svc.store.ListSlices(orgID) {
			cp := *r
			cp.Species = append([]string(nil), r.Species...)
			cp.TraitCodes = append([]string(nil), r.TraitCodes...)
			cp.ApprovedSubjects = append([]string(nil), r.ApprovedSubjects...)
			out = append(out, &cp)
		}
		return nil
	})
	return out
}

func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}
