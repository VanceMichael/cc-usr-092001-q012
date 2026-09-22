package service

import (
	"example.com/batch-092001-q012/internal/domain"
)

// resolveSubject 沿合并链解析个体编号，返回规范编号与个体记录。
// 调用方须已持有锁。
func (s *Service) resolveSubject(ref string) (string, domain.Subject, bool) {
	seen := map[string]bool{}
	current := ref
	for {
		merge, merged := s.st.Merges[current]
		if !merged {
			break
		}
		if seen[current] {
			return "", domain.Subject{}, false // 合并链成环，按未登记处理
		}
		seen[current] = true
		current = merge.CanonicalRef
	}
	subject, ok := s.st.Subjects[current]
	if !ok {
		return "", domain.Subject{}, false
	}
	return current, subject, true
}

// ResolveSubjectRef 对外暴露合并链解析结果。
func (s *Service) ResolveSubjectRef(ref string) (string, error) {
	s.st.Lock()
	defer s.st.Unlock()
	canonical, _, ok := s.resolveSubject(ref)
	if !ok {
		return "", fail("NOT_FOUND", "个体编号 %s 未登记", ref)
	}
	return canonical, nil
}

// MergeSubjects 把 fromRef 合并到 toRef（个体合并或标识纠错）。
// 只追加映射记录，历史观测与谱系引用保持原样，亲缘链通过解析保持连续。
func (s *Service) MergeSubjects(fromRef, toRef, reason string) (domain.SubjectMerge, error) {
	if fromRef == toRef {
		return domain.SubjectMerge{}, fail("INVALID_ARGUMENT", "不能合并到自身")
	}
	if reason == "" {
		return domain.SubjectMerge{}, fail("INVALID_ARGUMENT", "合并原因不能为空")
	}
	s.st.Lock()
	defer s.st.Unlock()
	if _, ok := s.st.Subjects[fromRef]; !ok {
		return domain.SubjectMerge{}, fail("NOT_FOUND", "个体编号 %s 未登记", fromRef)
	}
	if _, ok := s.st.Subjects[toRef]; !ok {
		return domain.SubjectMerge{}, fail("NOT_FOUND", "个体编号 %s 未登记", toRef)
	}
	if _, exists := s.st.Merges[fromRef]; exists {
		return domain.SubjectMerge{}, fail("CONFLICT", "个体编号 %s 已被合并", fromRef)
	}
	// 环检测：toRef 解析后不能回到 fromRef
	if canonical, _, ok := s.resolveSubject(toRef); ok && canonical == fromRef {
		return domain.SubjectMerge{}, fail("CONFLICT", "合并会形成环路")
	}
	merge := domain.SubjectMerge{
		MergedRef:    fromRef,
		CanonicalRef: toRef,
		Reason:       reason,
		MergedAt:     s.now(),
	}
	s.st.Merges[fromRef] = merge
	return merge, s.st.Save()
}

// PedigreeNode 是谱系视图中的一个节点，编号均为解析后的规范编号。
type PedigreeNode struct {
	Ref         string        `json:"ref"`
	SpeciesCode string        `json:"species_code"`
	Generation  int           `json:"generation"`
	Sire        *PedigreeNode `json:"sire,omitempty"`
	Dam         *PedigreeNode `json:"dam,omitempty"`
}

// Pedigree 返回个体的谱系视图；祖先引用沿合并链解析，保证亲缘链不中断。
func (s *Service) Pedigree(ref string, depth int) (*PedigreeNode, error) {
	if depth < 0 {
		depth = 0
	}
	s.st.Lock()
	defer s.st.Unlock()
	return s.buildPedigree(ref, depth, map[string]bool{})
}

func (s *Service) buildPedigree(ref string, depth int, visiting map[string]bool) (*PedigreeNode, error) {
	canonical, subject, ok := s.resolveSubject(ref)
	if !ok {
		return nil, fail("NOT_FOUND", "个体编号 %s 未登记", ref)
	}
	node := &PedigreeNode{
		Ref:         canonical,
		SpeciesCode: subject.SpeciesCode,
		Generation:  subject.Generation,
	}
	if depth == 0 || visiting[canonical] {
		return node, nil
	}
	visiting[canonical] = true
	if subject.SireRef != "" {
		if sire, err := s.buildPedigree(subject.SireRef, depth-1, visiting); err == nil {
			node.Sire = sire
		}
	}
	if subject.DamRef != "" {
		if dam, err := s.buildPedigree(subject.DamRef, depth-1, visiting); err == nil {
			node.Dam = dam
		}
	}
	delete(visiting, canonical)
	return node, nil
}
