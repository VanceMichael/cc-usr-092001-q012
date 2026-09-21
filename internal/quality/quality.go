// Package quality 提供跨机构质量反馈：拒收原因查询、确认与按设备的反馈流。
//
// 拒收记录在采集时自动产生，数据属主机构通过收件箱看到本机构全部设备的
// 拒收原因；联合体或其他参与方可按设备序列号查看跨机构反馈。
// 属主确认（ack）只表示已知悉，不删除记录。
package quality

import (
	"time"

	"example.com/batch-092001-q012/internal/apperr"
	"example.com/batch-092001-q012/internal/domain"
	"example.com/batch-092001-q012/internal/store"
)

// Service 是质量反馈服务。
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

// OrgInbox 返回某机构的拒收收件箱（按拒收时间倒序）。
func (svc *Service) OrgInbox(orgID string, onlyUnacked bool) ([]*domain.Rejection, error) {
	if orgID == "" {
		return nil, apperr.New(apperr.Validation, "机构编号不能为空")
	}
	out := []*domain.Rejection{}
	err := svc.store.View(func() error {
		out = cloneRejections(filterAndSort(svc.store.ListRejections(orgID, ""), onlyUnacked))
		return nil
	})
	return out, err
}

// DeviceFeedback 返回按设备的跨机构反馈流（任何参与方均可查看，
// 因为拒收原因与载荷不含真实身份信息）。
func (svc *Service) DeviceFeedback(serial string) ([]*domain.Rejection, error) {
	if serial == "" {
		return nil, apperr.New(apperr.Validation, "设备序列号不能为空")
	}
	out := []*domain.Rejection{}
	err := svc.store.View(func() error {
		out = cloneRejections(filterAndSort(svc.store.ListRejections("", serial), false))
		return nil
	})
	return out, err
}

// Ack 由数据属主确认一条拒收反馈。非属主机构不能确认他人记录。
func (svc *Service) Ack(orgID, rejectionID string) error {
	return svc.store.Update(func() error {
		r, ok := svc.store.GetRejection(rejectionID)
		if !ok {
			return apperr.New(apperr.NotFound, "拒收记录 %s 不存在", rejectionID)
		}
		if r.OrgID != orgID {
			return apperr.New(apperr.Forbidden, "仅数据属主机构可确认本机构的拒收记录")
		}
		svc.store.AckRejection(rejectionID)
		return nil
	})
}

// Get 取单条拒收记录。
func (svc *Service) Get(id string) (*domain.Rejection, error) {
	out := &domain.Rejection{}
	err := svc.store.View(func() error {
		r, ok := svc.store.GetRejection(id)
		if !ok {
			return apperr.New(apperr.NotFound, "拒收记录 %s 不存在", id)
		}
		*out = *r
		out.Reasons = append([]string(nil), r.Reasons...)
		return nil
	})
	return out, err
}

func cloneRejections(in []*domain.Rejection) []*domain.Rejection {
	out := make([]*domain.Rejection, len(in))
	for i, r := range in {
		cp := *r
		cp.Reasons = append([]string(nil), r.Reasons...)
		out[i] = &cp
	}
	return out
}

func filterAndSort(in []*domain.Rejection, onlyUnacked bool) []*domain.Rejection {
	out := make([]*domain.Rejection, 0, len(in))
	for _, r := range in {
		if onlyUnacked && r.AckedByOrg {
			continue
		}
		out = append(out, r)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].RejectedAt.Before(out[j].RejectedAt); j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
