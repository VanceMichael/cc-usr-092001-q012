// Package standards 负责标准版本的登记与生效选择。
//
// 关键不变量：
//   - 版本内容在登记时计算哈希并冻结，之后不可修改（没有更新接口）；
//   - 采集发生在哪个时间点，就绑定该时刻已生效的最新版本；
//   - 新版本生效不影响历史数据——历史记录持有的是 RulesHash 而非可变引用。
package standards

import (
	"time"

	"example.com/batch-092001-q012/internal/apperr"
	"example.com/batch-092001-q012/internal/canon"
	"example.com/batch-092001-q012/internal/domain"
	"example.com/batch-092001-q012/internal/store"
)

// Service 是标准版本服务。
type Service struct {
	store *store.Store
	now   func() time.Time
}

// New 创建服务，now 可注入以便测试生效边界。
func New(s *store.Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: s, now: now}
}

// RegisterInput 是登记新版本的入参。
type RegisterInput struct {
	StandardID   string
	Version      string
	EffectiveAt  time.Time
	Rules        domain.RuleSet
	RegisteredBy string
}

// Register 冻结并登记一个标准版本。
func (svc *Service) Register(in RegisterInput) (*domain.StandardVersion, error) {
	if in.StandardID == "" || in.Version == "" {
		return nil, apperr.New(apperr.Validation, "标准编号与版本号不能为空")
	}
	if in.EffectiveAt.IsZero() {
		return nil, apperr.New(apperr.Validation, "生效时间不能为空")
	}
	if err := validateRules(in.Rules); err != nil {
		return nil, err
	}
	hash, err := canon.Hash(in.Rules)
	if err != nil {
		return nil, apperr.New(apperr.Validation, "规则正文无法序列化: %v", err)
	}

	v := &domain.StandardVersion{
		StandardID:   in.StandardID,
		Version:      in.Version,
		EffectiveAt:  in.EffectiveAt,
		RegisteredBy: in.RegisteredBy,
		RegisteredAt: svc.now(),
		Status:       domain.StandardActive,
		Rules:        in.Rules,
		RulesHash:    hash,
	}
	var saved bool
	err = svc.store.Update(func() error {
		if existing, ok := svc.store.GetStandardVersion(in.StandardID, in.Version); ok {
			if existing.RulesHash != hash {
				return apperr.New(apperr.Conflict,
					"版本 %s/%s 已登记且规则内容不同；标准版本冻结后禁止修改", in.StandardID, in.Version)
			}
			return apperr.New(apperr.Conflict, "版本 %s/%s 已登记（幂等返回需走查询接口）", in.StandardID, in.Version)
		}
		// 生效时间不得早于同标准既有版本，避免"旧版本伪装成新版本"重释历史。
		for _, old := range svc.store.ListStandardVersions(in.StandardID) {
			if !in.EffectiveAt.After(old.EffectiveAt) {
				return apperr.New(apperr.Validation,
					"新版本生效时间 %s 必须晚于既有版本 %s 的生效时间 %s",
					in.EffectiveAt, old.Version, old.EffectiveAt)
			}
		}
		saved = svc.store.PutStandardVersion(v)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !saved {
		return nil, apperr.New(apperr.Conflict, "版本登记失败")
	}
	return v, nil
}

// EffectiveAt 返回采集发生时刻应适用的标准版本。
func (svc *Service) EffectiveAt(standardID string, at time.Time) (*domain.StandardVersion, error) {
	var picked *domain.StandardVersion
	err := svc.store.View(func() error {
		v, ok := svc.store.EffectiveStandard(standardID, at)
		if !ok {
			return apperr.New(apperr.NotFound, "标准 %s 在 %s 没有已生效版本", standardID, at)
		}
		picked = v
		return nil
	})
	return picked, err
}

// Get 按编号取版本。
func (svc *Service) Get(standardID, version string) (*domain.StandardVersion, error) {
	var out *domain.StandardVersion
	err := svc.store.View(func() error {
		v, ok := svc.store.GetStandardVersion(standardID, version)
		if !ok {
			return apperr.New(apperr.NotFound, "标准版本 %s/%s 不存在", standardID, version)
		}
		out = v
		return nil
	})
	return out, err
}

// ByHash 按规则哈希反查（溯源核对）。
func (svc *Service) ByHash(hash string) (*domain.StandardVersion, error) {
	var out *domain.StandardVersion
	err := svc.store.View(func() error {
		v, ok := svc.store.StandardByHash(hash)
		if !ok {
			return apperr.New(apperr.NotFound, "规则哈希 %s 无对应登记版本", hash)
		}
		out = v
		return nil
	})
	return out, err
}

// Retire 停用版本；不影响已绑定该版本哈希的历史记录。
func (svc *Service) Retire(standardID, version string) error {
	return svc.store.Update(func() error {
		if !svc.store.RetireStandardVersion(standardID, version) {
			return apperr.New(apperr.NotFound, "标准版本 %s/%s 不存在", standardID, version)
		}
		return nil
	})
}

// Trait 在规则集中按物种与性状编码查找定义。
func Trait(rules domain.RuleSet, species, code string) (domain.TraitDef, bool) {
	for _, t := range rules.Traits {
		if t.Species == species && t.Code == code {
			return t, true
		}
	}
	return domain.TraitDef{}, false
}

func validateRules(rules domain.RuleSet) error {
	if len(rules.Species) == 0 {
		return apperr.New(apperr.Validation, "规则至少覆盖一个物种")
	}
	if len(rules.Traits) == 0 {
		return apperr.New(apperr.Validation, "规则至少定义一个性状")
	}
	seen := map[string]bool{}
	for _, t := range rules.Traits {
		key := t.Species + "/" + t.Code
		if seen[key] {
			return apperr.New(apperr.Validation, "性状定义重复: %s", key)
		}
		seen[key] = true
		if t.Unit == "" {
			return apperr.New(apperr.Validation, "性状 %s 缺少规范单位", key)
		}
		if t.Decimals < 0 {
			return apperr.New(apperr.Validation, "性状 %s 小数位数不能为负", key)
		}
		if !t.MinValue.IsZero() && !t.MaxValue.IsZero() && t.MinValue.Cmp(t.MaxValue) > 0 {
			return apperr.New(apperr.Validation, "性状 %s 的最小值大于最大值", key)
		}
		if t.MinAgeDays < 0 || t.MaxAgeDays < 0 {
			return apperr.New(apperr.Validation, "性状 %s 的日龄窗口不能为负", key)
		}
		if t.MaxAgeDays != 0 && t.MinAgeDays > t.MaxAgeDays {
			return apperr.New(apperr.Validation, "性状 %s 的日龄窗口倒置", key)
		}
	}
	return nil
}
