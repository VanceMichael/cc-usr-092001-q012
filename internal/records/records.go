// Package records 提供原始读数的查询、修订与质量标记入口。
//
// 三类对象严格分离：
//   - Reading 一旦接收即不可变，没有任何修改接口；
//   - Revision 是并列保存的新值，通过 supersedes 形成修订链，原值始终保留；
//   - QualityFlag 是独立的质量判定，可被提出方之外的机构（如联合体质检）
//     跨机构挂到他人数据上，并由数据属主或质检方处理闭环。
package records

import (
	"time"

	"example.com/batch-092001-q012/internal/apperr"
	"example.com/batch-092001-q012/internal/canon"
	"example.com/batch-092001-q012/internal/domain"
	"example.com/batch-092001-q012/internal/store"
)

// Service 是读数/修订/质量标记服务。
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

// AddRevisionInput 修订入参。
type AddRevisionInput struct {
	ReadingID string
	NewValue  string
	Reason    string
	ByOrgID   string
}

// AddRevision 为读数追加一个修订值。不校验新值是否合规于当前规则——
// 修订依据的是采集时绑定的规则版本，调用方（API 层）可另行复核；
// 这里只保证十进制可解析、原因必填，以及修订链不可断裂。
func (svc *Service) AddRevision(in AddRevisionInput) (*domain.Revision, error) {
	if in.ReadingID == "" || in.NewValue == "" || in.Reason == "" || in.ByOrgID == "" {
		return nil, apperr.New(apperr.Validation, "读数编号、新值、原因与操作机构均不能为空")
	}
	if _, err := canon.ParseDecimal(in.NewValue); err != nil {
		return nil, apperr.New(apperr.Validation, "修订值必须为十进制字符串: %v", err)
	}
	rev := &domain.Revision{
		ID:        store.NewID("REV"),
		ReadingID: in.ReadingID,
		NewValue:  in.NewValue,
		Reason:    in.Reason,
		ByOrgID:   in.ByOrgID,
		CreatedAt: svc.now(),
	}
	err := svc.store.Update(func() error {
		r, ok := svc.store.GetReading(in.ReadingID)
		if !ok {
			return apperr.New(apperr.NotFound, "读数 %s 不存在", in.ReadingID)
		}
		if r.Status != domain.RecordAccepted {
			return apperr.New(apperr.Validation, "仅已接收读数可修订，当前状态 %s", r.Status)
		}
		if r.OrgID != in.ByOrgID {
			return apperr.New(apperr.Forbidden,
				"读数 %s 属于机构 %s：跨机构质量意见请使用质量标记而非修订", in.ReadingID, r.OrgID)
		}
		svc.store.AppendRevision(rev)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rev, nil
}

// AddFlagInput 质量标记入参。
type AddFlagInput struct {
	ReadingID string
	Severity  string
	Code      string
	Detail    string
	ByOrgID   string
}

// AddFlag 追加质量标记。允许非数据属主机构挂标记，形成跨机构质量反馈。
func (svc *Service) AddFlag(in AddFlagInput) (*domain.QualityFlag, error) {
	if in.ReadingID == "" || in.Code == "" || in.ByOrgID == "" {
		return nil, apperr.New(apperr.Validation, "读数编号、标记代码与提出机构不能为空")
	}
	switch in.Severity {
	case "info", "warning", "error":
	default:
		return nil, apperr.New(apperr.Validation, "严重级别只接受 info/warning/error")
	}
	f := &domain.QualityFlag{
		ID:        store.NewID("FLAG"),
		ReadingID: in.ReadingID,
		Severity:  in.Severity,
		Code:      in.Code,
		Detail:    in.Detail,
		ByOrgID:   in.ByOrgID,
		CreatedAt: svc.now(),
		Status:    domain.QualityOpen,
	}
	err := svc.store.Update(func() error {
		if _, ok := svc.store.GetReading(in.ReadingID); !ok {
			return apperr.New(apperr.NotFound, "读数 %s 不存在", in.ReadingID)
		}
		svc.store.AppendFlag(f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

// ResolveFlag 处理质量标记：成立则 resolved，不成立则 rejected，均需说明。
// 只有标记提出方或被标数据的属主机构可以处理。
func (svc *Service) ResolveFlag(flagID, byOrgID, resolution string, reject bool) (*domain.QualityFlag, error) {
	if resolution == "" {
		return nil, apperr.New(apperr.Validation, "处理质量标记必须填写处理结论")
	}
	var out *domain.QualityFlag
	err := svc.store.Update(func() error {
		f := svc.store.GetFlag(flagID)
		if f == nil {
			return apperr.New(apperr.NotFound, "质量标记 %s 不存在", flagID)
		}
		r, ok := svc.store.GetReading(f.ReadingID)
		if !ok {
			return apperr.New(apperr.NotFound, "质量标记关联读数 %s 不存在", f.ReadingID)
		}
		if f.ByOrgID != byOrgID && r.OrgID != byOrgID {
			return apperr.New(apperr.Forbidden, "仅标记提出方或数据属主可处理该标记")
		}
		if f.Status != domain.QualityOpen {
			return apperr.New(apperr.Conflict, "质量标记 %s 已处理", flagID)
		}
		if reject {
			f.Status = domain.QualityRejected
		} else {
			f.Status = domain.QualityResolved
		}
		f.Resolution = resolution
		out = f
		return nil
	})
	return out, err
}

// GetReading 取读数。
func (svc *Service) GetReading(id string) (*domain.Reading, error) {
	var out *domain.Reading
	err := svc.store.View(func() error {
		r, ok := svc.store.GetReading(id)
		if !ok {
			return apperr.New(apperr.NotFound, "读数 %s 不存在", id)
		}
		out = r
		return nil
	})
	return out, err
}

// ReadingView 是读数的三层视图：原始值 + 修订链 + 质量标记。
type ReadingView struct {
	Reading        *domain.Reading       `json:"reading"`
	Revisions      []*domain.Revision    `json:"revisions"`
	Flags          []*domain.QualityFlag `json:"flags"`
	EffectiveValue string                `json:"effective_value"`
}

// View 返回读数及其修订与质量标记（三表联查，不改写任何对象）。
// 仅数据属主机构可查看完整视图；跨机构质量协作通过质量反馈接口进行。
func (svc *Service) View(id, actorOrgID string) (*ReadingView, error) {
	out := &ReadingView{}
	err := svc.store.View(func() error {
		r, ok := svc.store.GetReading(id)
		if !ok {
			return apperr.New(apperr.NotFound, "读数 %s 不存在", id)
		}
		if r.OrgID != actorOrgID {
			return apperr.New(apperr.Forbidden, "读数 %s 不属于机构 %s", id, actorOrgID)
		}
		out.Reading = r
		out.Revisions = svc.store.ListRevisions(id)
		out.Flags = svc.store.ListFlags(id)
		out.EffectiveValue = svc.store.EffectiveValueText(r)
		return nil
	})
	return out, err
}
