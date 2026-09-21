// Package subjects 管理个体登记、机构本地编号别名、亲缘链以及合并/纠错。
//
// 不断链原则：
//   - 合并不删除任何个体，只在被合并个体上立 MergedInto 墓碑指针；
//   - 指向被合并个体的亲子边与本地别名原子地改写指向存续个体；
//   - 全部改动产生 IdentityEvent / LineageEvent，旧编号永远可解析到新个体；
//   - 谱系边改写前后都做祖先环检测，纠错只能改边，不能制造环。
package subjects

import (
	"time"

	"example.com/batch-092001-q012/internal/apperr"
	"example.com/batch-092001-q012/internal/domain"
	"example.com/batch-092001-q012/internal/store"
)

// Service 是个体谱系服务。
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

// RegisterInput 登记个体入参。
type RegisterInput struct {
	ID         string
	Species    string
	Dob        *time.Time
	SireID     string
	DamID      string
	Generation int
	Sex        string
	ByOrgID    string
}

// Register 登记规范个体并可选地建立初始亲子边（亲本编号会先解析到存续个体）。
func (svc *Service) Register(in RegisterInput) (*domain.Animal, error) {
	if in.ID == "" || in.Species == "" {
		return nil, apperr.New(apperr.Validation, "个体编号与物种不能为空")
	}
	if in.Sex != "" && in.Sex != "M" && in.Sex != "F" {
		return nil, apperr.New(apperr.Validation, "性别只接受 M/F/空")
	}
	a := &domain.Animal{
		ID:         in.ID,
		Species:    in.Species,
		Dob:        in.Dob,
		Generation: in.Generation,
		Sex:        in.Sex,
		Active:     true,
		CreatedAt:  svc.now(),
	}
	err := svc.store.Update(func() error {
		if _, exists := svc.store.GetAnimal(in.ID); exists {
			return apperr.New(apperr.Conflict, "个体 %s 已登记", in.ID)
		}
		for _, parent := range []string{in.SireID, in.DamID} {
			if parent == "" {
				continue
			}
			p, ok := svc.store.ResolveAnimal(parent)
			if !ok {
				return apperr.New(apperr.Validation, "亲本 %s 不存在", parent)
			}
			if p.ID == in.ID {
				return apperr.New(apperr.LineageCycle, "个体不能以自身为亲本")
			}
			if p.Species != in.Species {
				return apperr.New(apperr.Validation, "亲本 %s 物种与子代不一致", parent)
			}
		}
		if in.SireID != "" {
			p, _ := svc.store.ResolveAnimal(in.SireID)
			a.SireID = p.ID
		}
		if in.DamID != "" {
			p, _ := svc.store.ResolveAnimal(in.DamID)
			a.DamID = p.ID
		}
		svc.store.PutAnimal(a)
		if a.SireID != "" || a.DamID != "" {
			svc.store.AppendLineageEvent(&domain.LineageEvent{
				ID:       store.NewID("LIN"),
				AnimalID: a.ID,
				Kind:     "link",
				NewSire:  a.SireID,
				NewDam:   a.DamID,
				Reason:   "登记时建立亲子边",
				ByOrgID:  in.ByOrgID,
				At:       svc.now(),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}

// BindAlias 把机构本地编号绑定到规范个体；同一本地编号不可直接改绑
// （纠错请走 CorrectAlias，留下审计轨迹）。
func (svc *Service) BindAlias(orgID, localID, canonicalID string) error {
	if orgID == "" || localID == "" || canonicalID == "" {
		return apperr.New(apperr.Validation, "机构、本地编号与规范编号均不能为空")
	}
	return svc.store.Update(func() error {
		if existing, ok := svc.store.ResolveAlias(orgID, localID); ok {
			if existing.CanonicalID == canonicalID {
				return nil // 幂等
			}
			return apperr.New(apperr.Conflict,
				"本地编号 %s/%s 已绑定 %s；纠错请走专用接口", orgID, localID, existing.CanonicalID)
		}
		target, ok := svc.store.ResolveAnimal(canonicalID)
		if !ok {
			return apperr.New(apperr.NotFound, "规范个体 %s 不存在", canonicalID)
		}
		if !svc.store.PutAlias(&domain.Alias{
			OrgID:       orgID,
			LocalID:     localID,
			CanonicalID: target.ID,
			BoundAt:     svc.now(),
		}) {
			return apperr.New(apperr.Conflict, "别名绑定冲突")
		}
		return nil
	})
}

// CorrectAlias 纠正机构本地编号的错误绑定。旧关系通过 IdentityEvent 保留，
// 历史采集记录仍可用旧本地编号追溯。
func (svc *Service) CorrectAlias(orgID, localID, wantCanonical, reason string) error {
	if reason == "" {
		return apperr.New(apperr.Validation, "标识纠错必须填写原因")
	}
	return svc.store.Update(func() error {
		old, ok := svc.store.ResolveAlias(orgID, localID)
		if !ok {
			return apperr.New(apperr.NotFound, "本地编号 %s/%s 未绑定", orgID, localID)
		}
		target, exists := svc.store.ResolveAnimal(wantCanonical)
		if !exists {
			return apperr.New(apperr.NotFound, "目标规范个体 %s 不存在", wantCanonical)
		}
		if old.CanonicalID == target.ID {
			return apperr.New(apperr.Conflict, "纠错目标与当前绑定相同")
		}
		svc.store.AppendIdentityEvent(&domain.IdentityEvent{
			ID:       store.NewID("IDE"),
			Kind:     domain.CorrectEvent,
			FromID:   old.CanonicalID,
			ToID:     target.ID,
			OldLabel: orgID + "/" + localID,
			NewLabel: orgID + "/" + localID,
			Reason:   reason,
			ByOrgID:  orgID,
			At:       svc.now(),
		})
		svc.store.RebindAlias(orgID, localID, target.ID)
		return nil
	})
}

// CorrectLineage 纠正亲子边。旧边进入 LineageEvent，并在提交前做环检测。
// 传 nil 表示该亲本保持不变；传指向空字符串的指针表示清空该亲本。
func (svc *Service) CorrectLineage(animalID string, newSire, newDam *string, reason, byOrg string) error {
	if reason == "" {
		return apperr.New(apperr.Validation, "谱系纠错必须填写原因")
	}
	return svc.store.Update(func() error {
		animal, ok := svc.store.ResolveAnimal(animalID)
		if !ok {
			return apperr.New(apperr.NotFound, "个体 %s 不存在", animalID)
		}
		resolvedSire := animal.SireID
		if newSire != nil {
			resolvedSire = *newSire
			if resolvedSire != "" {
				p, ok := svc.store.ResolveAnimal(resolvedSire)
				if !ok {
					return apperr.New(apperr.NotFound, "父本 %s 不存在", resolvedSire)
				}
				resolvedSire = p.ID
			}
		}
		resolvedDam := animal.DamID
		if newDam != nil {
			resolvedDam = *newDam
			if resolvedDam != "" {
				p, ok := svc.store.ResolveAnimal(resolvedDam)
				if !ok {
					return apperr.New(apperr.NotFound, "母本 %s 不存在", resolvedDam)
				}
				resolvedDam = p.ID
			}
		}
		if createsCycle(svc.store, animal.ID, resolvedSire, resolvedDam) {
			return apperr.New(apperr.LineageCycle, "谱系纠错会使 %s 的祖先链成环", animal.ID)
		}
		oldSire, oldDam := animal.SireID, animal.DamID
		animal.SireID = resolvedSire
		animal.DamID = resolvedDam
		svc.store.AppendLineageEvent(&domain.LineageEvent{
			ID:       store.NewID("LIN"),
			AnimalID: animal.ID,
			Kind:     "correction",
			OldSire:  oldSire,
			NewSire:  animal.SireID,
			OldDam:   oldDam,
			NewDam:   animal.DamID,
			Reason:   reason,
			ByOrgID:  byOrg,
			At:       svc.now(),
		})
		return nil
	})
}

// Merge 把重复个体 fromID 合并进存续个体 intoID。
// 语义：
//   - fromID 立墓碑 MergedInto=intoID，编号永久保留、可解析；
//   - 所有原指向 fromID 的亲子边改写指向 intoID；
//   - 机构别名整体迁移，fromID 缺失的亲本边补到 intoID；
//   - 记录 IdentityEvent / LineageEvent；双向祖先检测保证谱系仍是 DAG。
func (svc *Service) Merge(fromID, intoID, reason, byOrg string) error {
	if reason == "" {
		return apperr.New(apperr.Validation, "合并必须填写原因")
	}
	return svc.store.Update(func() error {
		from, ok := svc.store.ResolveAnimal(fromID)
		if !ok {
			return apperr.New(apperr.NotFound, "被合并个体 %s 不存在", fromID)
		}
		into, ok := svc.store.ResolveAnimal(intoID)
		if !ok {
			return apperr.New(apperr.NotFound, "存续个体 %s 不存在", intoID)
		}
		if from.ID == into.ID {
			return apperr.New(apperr.Conflict, "个体不能与自身合并")
		}
		if from.Species != into.Species {
			return apperr.New(apperr.Validation, "仅允许合并同一物种的个体")
		}
		// 任一方向的祖先关系都会在边改写后形成自环，必须双向拒绝。
		if isAncestor(svc.store, from.ID, into.ID) || isAncestor(svc.store, into.ID, from.ID) {
			return apperr.New(apperr.LineageCycle,
				"%s 与 %s 存在直系亲缘关系，合并会破坏谱系", from.ID, into.ID)
		}

		// 1) 子代边改写：先记录旧值再改，逐条留痕。
		for _, anim := range svc.store.ListAllAnimals() {
			oldSire, oldDam := anim.SireID, anim.DamID
			if oldSire != from.ID && oldDam != from.ID {
				continue
			}
			if oldSire == from.ID {
				anim.SireID = into.ID
			}
			if oldDam == from.ID {
				anim.DamID = into.ID
			}
			svc.store.AppendLineageEvent(&domain.LineageEvent{
				ID:       store.NewID("LIN"),
				AnimalID: anim.ID,
				Kind:     "correction",
				OldSire:  oldSire,
				NewSire:  anim.SireID,
				OldDam:   oldDam,
				NewDam:   anim.DamID,
				Reason:   "因个体合并 " + from.ID + " -> " + into.ID + " 改写亲子边",
				ByOrgID:  byOrg,
				At:       svc.now(),
			})
		}
		// 2) into 缺失的亲本边由 from 补齐，并留痕。
		supplementedSire := into.SireID == "" && from.SireID != ""
		supplementedDam := into.DamID == "" && from.DamID != ""
		if supplementedSire {
			into.SireID = from.SireID
		}
		if supplementedDam {
			into.DamID = from.DamID
		}
		if supplementedSire || supplementedDam {
			svc.store.AppendLineageEvent(&domain.LineageEvent{
				ID:       store.NewID("LIN"),
				AnimalID: into.ID,
				Kind:     "correction",
				OldSire:  "",
				NewSire:  into.SireID,
				OldDam:   "",
				NewDam:   into.DamID,
				Reason:   "因合并 " + from.ID + " -> " + into.ID + " 补入亲本边",
				ByOrgID:  byOrg,
				At:       svc.now(),
			})
		}
		// 3) 别名迁移。
		for _, al := range svc.store.AliasesOf(from.ID) {
			svc.store.RebindAlias(al.OrgID, al.LocalID, into.ID)
		}
		// 4) 墓碑与审计事件。
		from.MergedInto = into.ID
		from.Active = false
		svc.store.AppendIdentityEvent(&domain.IdentityEvent{
			ID:      store.NewID("IDE"),
			Kind:    domain.MergeEvent,
			FromID:  from.ID,
			ToID:    into.ID,
			Reason:  reason,
			ByOrgID: byOrg,
			At:      svc.now(),
		})
		return nil
	})
}

// Resolve 把任意历史编号（含已合并个体的旧号、机构本地号）解析到当前存续个体。
func (svc *Service) Resolve(orgID, ref string) (*domain.Animal, error) {
	var out *domain.Animal
	err := svc.store.View(func() error {
		if orgID != "" {
			if al, ok := svc.store.ResolveAlias(orgID, ref); ok {
				a, _ := svc.store.ResolveAnimal(al.CanonicalID)
				out = a
				return nil
			}
		}
		a, ok := svc.store.ResolveAnimal(ref)
		if !ok {
			return apperr.New(apperr.UnknownSubject, "编号 %s 无法解析到任何个体", ref)
		}
		out = a
		return nil
	})
	return out, err
}

// Get 取个体原始档案（可能为已合并墓碑）。
func (svc *Service) Get(id string) (*domain.Animal, error) {
	var out *domain.Animal
	err := svc.store.View(func() error {
		a, ok := svc.store.GetAnimal(id)
		if !ok {
			return apperr.New(apperr.NotFound, "个体 %s 不存在", id)
		}
		out = a
		return nil
	})
	return out, err
}

// LineageHistory 返回个体的谱系边变更历史。
func (svc *Service) LineageHistory(id string) ([]*domain.LineageEvent, error) {
	var out []*domain.LineageEvent
	err := svc.store.View(func() error {
		a, ok := svc.store.ResolveAnimal(id)
		if !ok {
			return apperr.New(apperr.NotFound, "个体 %s 不存在", id)
		}
		out = svc.store.ListLineageEvents(a.ID)
		return nil
	})
	return out, err
}

// createsCycle 假设给 child 设置新父/母边后，向上遍历检查是否能回到 child。
// 被检查的新边本身是 child -> newParent：从 newParent 沿现有亲子链上行，
// 一旦到达 child 即说明 newParent 是 child 的后代，构成环。
func createsCycle(s *store.Store, childID, sireID, damID string) bool {
	reach := func(start string) bool {
		seen := map[string]bool{}
		stack := []string{start}
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if cur == childID {
				return true
			}
			if seen[cur] {
				continue
			}
			seen[cur] = true
			a, ok := s.ResolveAnimal(cur)
			if !ok {
				continue
			}
			if a.SireID != "" {
				stack = append(stack, a.SireID)
			}
			if a.DamID != "" {
				stack = append(stack, a.DamID)
			}
		}
		return false
	}
	if sireID != "" && (sireID == childID || reach(sireID)) {
		return true
	}
	if damID != "" && (damID == childID || reach(damID)) {
		return true
	}
	return false
}

// isAncestor 判断 candidate 是否为 descendantID 的祖先（沿父/母边向上）。
func isAncestor(s *store.Store, descendantID, candidate string) bool {
	seen := map[string]bool{}
	stack := []string{descendantID}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		a, ok := s.ResolveAnimal(cur)
		if !ok {
			continue
		}
		for _, p := range []string{a.SireID, a.DamID} {
			if p == candidate {
				return true
			}
			if p != "" {
				stack = append(stack, p)
			}
		}
	}
	return false
}
