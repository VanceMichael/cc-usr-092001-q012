// Package store 提供网关的并发安全内存存储。
//
// 所有写操作必须经过 Update 闭包，读操作经过 View 闭包；
// 闭包内不得执行外部 IO，也不得嵌套获取另一把锁，避免长时间持锁。
// 生产环境可替换为持久化实现，领域服务只依赖这里暴露的索引方法。
package store

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"example.com/batch-092001-q012/internal/domain"
)

// DedupKey 是离线补传去重的唯一键：来源序号只在同一设备内递增。
type DedupKey struct {
	DeviceSerial   string
	SourceSequence int64
}

// AliasKey 是机构本地编号的唯一键。
type AliasKey struct {
	OrgID   string
	LocalID string
}

// Store 集中持有全部实体映射与派生索引。
type Store struct {
	mu sync.RWMutex

	orgs           map[string]*domain.Org
	devices        map[string]*domain.Device
	calibs         map[string]*domain.Calibration
	calibsByDevice map[string][]string

	// standards: standardID -> version -> 冻结版本
	standards map[string]map[string]*domain.StandardVersion

	animals        map[string]*domain.Animal
	aliases        map[AliasKey]*domain.Alias
	lineageEvents  []*domain.LineageEvent
	identityEvents []*domain.IdentityEvent

	readings     map[string]*domain.Reading
	readingByKey map[DedupKey]string
	revisions    map[string][]*domain.Revision
	flags        map[string][]*domain.QualityFlag

	batches            map[string]*domain.Batch
	rejections         map[string]*domain.Rejection
	rejectionsByOrg    map[string][]string
	rejectionsByDevice map[string][]string

	slices    map[string]*domain.SliceRequest
	manifests map[string]*domain.DatasetManifest
}

// New 创建空存储。
func New() *Store {
	return &Store{
		orgs:               map[string]*domain.Org{},
		devices:            map[string]*domain.Device{},
		calibs:             map[string]*domain.Calibration{},
		calibsByDevice:     map[string][]string{},
		standards:          map[string]map[string]*domain.StandardVersion{},
		animals:            map[string]*domain.Animal{},
		aliases:            map[AliasKey]*domain.Alias{},
		readings:           map[string]*domain.Reading{},
		readingByKey:       map[DedupKey]string{},
		revisions:          map[string][]*domain.Revision{},
		flags:              map[string][]*domain.QualityFlag{},
		batches:            map[string]*domain.Batch{},
		rejections:         map[string]*domain.Rejection{},
		rejectionsByOrg:    map[string][]string{},
		rejectionsByDevice: map[string][]string{},
		slices:             map[string]*domain.SliceRequest{},
		manifests:          map[string]*domain.DatasetManifest{},
	}
}

var idCounter atomic.Int64

// NewID 在写事务内生成带前缀的有序编号（时间戳+进程内计数器）。
func NewID(prefix string) string {
	n := idCounter.Add(1)
	return fmt.Sprintf("%s-%d-%04d", prefix, time.Now().UnixNano(), n%10000)
}

// View 在读锁下执行查询。
func (s *Store) View(fn func() error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fn()
}

// Update 在写锁下执行修改，fn 返回错误时调用方自行保证不产生半成品
// （服务层约定：先全部校验、再落库，因此闭体内不再回滚）。
func (s *Store) Update(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn()
}

// ---- 机构 ----

func (s *Store) PutOrg(o *domain.Org) { s.orgs[o.ID] = o }
func (s *Store) GetOrg(id string) (*domain.Org, bool) {
	o, ok := s.orgs[id]
	return o, ok
}
func (s *Store) ListOrgs() []*domain.Org {
	out := make([]*domain.Org, 0, len(s.orgs))
	for _, o := range s.orgs {
		out = append(out, o)
	}
	return out
}

// ---- 标准版本 ----

// PutStandardVersion 登记冻结版本；同一标准内版本号重复时返回 false。
func (s *Store) PutStandardVersion(v *domain.StandardVersion) bool {
	versions, ok := s.standards[v.StandardID]
	if !ok {
		versions = map[string]*domain.StandardVersion{}
		s.standards[v.StandardID] = versions
	}
	if _, exists := versions[v.Version]; exists {
		return false
	}
	versions[v.Version] = v
	return true
}

// GetStandardVersion 取指定版本。
func (s *Store) GetStandardVersion(id, version string) (*domain.StandardVersion, bool) {
	if versions, ok := s.standards[id]; ok {
		v, found := versions[version]
		return v, found
	}
	return nil, false
}

// EffectiveStandard 选择在 at 时刻已生效（effective_at <= at）且
// at 不早于 not_before 约束、状态为 active 的最新版本；
// 没有任何版本时返回 false。选择只按生效时间排序，与登记时间无关。
func (s *Store) EffectiveStandard(standardID string, at time.Time) (*domain.StandardVersion, bool) {
	versions := s.standards[standardID]
	var picked *domain.StandardVersion
	for _, v := range versions {
		if v.Status != domain.StandardActive {
			continue
		}
		if v.EffectiveAt.After(at) {
			continue
		}
		if picked == nil || v.EffectiveAt.After(picked.EffectiveAt) {
			picked = v
		}
	}
	return picked, picked != nil
}

// StandardByHash 跨标准按规则哈希反查（溯源核对用）。
func (s *Store) StandardByHash(hash string) (*domain.StandardVersion, bool) {
	for _, versions := range s.standards {
		for _, v := range versions {
			if v.RulesHash == hash {
				return v, true
			}
		}
	}
	return nil, false
}

// ListStandardVersions 列出某标准全部版本（按生效时间升序）。
func (s *Store) ListStandardVersions(standardID string) []*domain.StandardVersion {
	out := []*domain.StandardVersion{}
	for _, v := range s.standards[standardID] {
		out = append(out, v)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].EffectiveAt.After(out[j].EffectiveAt); j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// RetireStandardVersion 停用版本（历史记录仍可通过哈希引用）。
func (s *Store) RetireStandardVersion(id, version string) bool {
	if v, ok := s.GetStandardVersion(id, version); ok {
		v.Status = domain.StandardRetired
		return true
	}
	return false
}

// ---- 设备与校准 ----

func (s *Store) PutDevice(d *domain.Device) { s.devices[d.Serial] = d }
func (s *Store) GetDevice(serial string) (*domain.Device, bool) {
	d, ok := s.devices[serial]
	return d, ok
}
func (s *Store) ListDevices() []*domain.Device {
	out := make([]*domain.Device, 0, len(s.devices))
	for _, d := range s.devices {
		out = append(out, d)
	}
	return out
}

func (s *Store) PutCalibration(c *domain.Calibration) {
	s.calibs[c.ID] = c
	s.calibsByDevice[c.DeviceSerial] = append(s.calibsByDevice[c.DeviceSerial], c.ID)
}
func (s *Store) GetCalibration(id string) (*domain.Calibration, bool) {
	c, ok := s.calibs[id]
	return c, ok
}

// CalibrationAt 返回覆盖 at 时刻的最新一张证书及其状态。
// 选择规则：issued_at <= at，按 issued_at 取最新；吊销优先于有效期判定。
func (s *Store) CalibrationAt(serial string, at time.Time) (*domain.Calibration, string, bool) {
	var picked *domain.Calibration
	for _, id := range s.calibsByDevice[serial] {
		c := s.calibs[id]
		if c.IssuedAt.After(at) {
			continue
		}
		if picked == nil || c.IssuedAt.After(picked.IssuedAt) {
			picked = c
		}
	}
	if picked == nil {
		return nil, "", false
	}
	status := domain.CalibrationValid
	switch {
	case picked.RevokedAt != nil && !picked.RevokedAt.After(at):
		status = domain.CalibrationRevoked
	case !picked.ExpiresAt.IsZero() && !at.Before(picked.ExpiresAt):
		status = domain.CalibrationDead
	case !picked.DueAt.IsZero() && !at.Before(picked.DueAt):
		status = domain.CalibrationDue
	}
	return picked, status, true
}

// ---- 个体与谱系 ----

func (s *Store) PutAnimal(a *domain.Animal) { s.animals[a.ID] = a }
func (s *Store) GetAnimal(id string) (*domain.Animal, bool) {
	a, ok := s.animals[id]
	return a, ok
}

// ListAllAnimals 返回全部个体档案（含已合并墓碑），仅供持锁的服务层遍历。
func (s *Store) ListAllAnimals() []*domain.Animal {
	out := make([]*domain.Animal, 0, len(s.animals))
	for _, a := range s.animals {
		out = append(out, a)
	}
	return out
}

// ResolveAnimal 沿 MergedInto 指针解析到当前存续个体。
// 指针链由合并服务保证无环，这里仍加深度上限防御。
func (s *Store) ResolveAnimal(id string) (*domain.Animal, bool) {
	a, ok := s.animals[id]
	if !ok {
		return nil, false
	}
	depth := 0
	for a.MergedInto != "" && depth < 10000 {
		next, found := s.animals[a.MergedInto]
		if !found {
			return a, true // 链断时返回当前节点，不丢数据
		}
		a = next
		depth++
	}
	return a, true
}

func (s *Store) PutAlias(a *domain.Alias) bool {
	key := AliasKey{OrgID: a.OrgID, LocalID: a.LocalID}
	if _, exists := s.aliases[key]; exists {
		return false
	}
	s.aliases[key] = a
	return true
}

// RebindAlias 在合并/纠错时把本地别名重新指向存续个体。
func (s *Store) RebindAlias(orgID, localID, canonicalID string) bool {
	key := AliasKey{OrgID: orgID, LocalID: localID}
	a, ok := s.aliases[key]
	if !ok {
		return false
	}
	a.CanonicalID = canonicalID
	return true
}

func (s *Store) ResolveAlias(orgID, localID string) (*domain.Alias, bool) {
	a, ok := s.aliases[AliasKey{OrgID: orgID, LocalID: localID}]
	return a, ok
}

// AliasesOf 列出指向某规范个体的全部本地别名（合并重写后仍可追溯）。
func (s *Store) AliasesOf(canonicalID string) []*domain.Alias {
	out := []*domain.Alias{}
	for _, a := range s.aliases {
		if a.CanonicalID == canonicalID {
			out = append(out, a)
		}
	}
	return out
}

func (s *Store) AppendLineageEvent(e *domain.LineageEvent) {
	s.lineageEvents = append(s.lineageEvents, e)
}
func (s *Store) ListLineageEvents(animalID string) []*domain.LineageEvent {
	out := []*domain.LineageEvent{}
	for _, e := range s.lineageEvents {
		if e.AnimalID == animalID {
			out = append(out, e)
		}
	}
	return out
}

func (s *Store) AppendIdentityEvent(e *domain.IdentityEvent) {
	s.identityEvents = append(s.identityEvents, e)
}
func (s *Store) ListIdentityEvents() []*domain.IdentityEvent {
	out := make([]*domain.IdentityEvent, len(s.identityEvents))
	copy(out, s.identityEvents)
	return out
}

// ---- 采集 ----

func (s *Store) PutReading(r *domain.Reading) {
	s.readings[r.ID] = r
	s.readingByKey[DedupKey{DeviceSerial: r.DeviceSerial, SourceSequence: r.SourceSequence}] = r.ID
}
func (s *Store) GetReading(id string) (*domain.Reading, bool) {
	r, ok := s.readings[id]
	return r, ok
}
func (s *Store) ReadingByDedup(serial string, seq int64) (*domain.Reading, bool) {
	id, ok := s.readingByKey[DedupKey{DeviceSerial: serial, SourceSequence: seq}]
	if !ok {
		return nil, false
	}
	return s.readings[id], true
}
func (s *Store) ListReadings() []*domain.Reading {
	out := make([]*domain.Reading, 0, len(s.readings))
	for _, r := range s.readings {
		out = append(out, r)
	}
	return out
}

func (s *Store) AppendRevision(rev *domain.Revision) {
	list := s.revisions[rev.ReadingID]
	if len(list) > 0 {
		rev.Supersedes = list[len(list)-1].ID
	}
	s.revisions[rev.ReadingID] = append(list, rev)
}
func (s *Store) ListRevisions(readingID string) []*domain.Revision {
	src := s.revisions[readingID]
	out := make([]*domain.Revision, len(src))
	copy(out, src)
	return out
}

// EffectiveValueText 返回读数当前生效值：原始值或最新修订值。
func (s *Store) EffectiveValueText(r *domain.Reading) string {
	if list := s.revisions[r.ID]; len(list) > 0 {
		return list[len(list)-1].NewValue
	}
	return r.ValueText
}

func (s *Store) AppendFlag(f *domain.QualityFlag) {
	s.flags[f.ReadingID] = append(s.flags[f.ReadingID], f)
}
func (s *Store) ListFlags(readingID string) []*domain.QualityFlag {
	src := s.flags[readingID]
	out := make([]*domain.QualityFlag, len(src))
	copy(out, src)
	return out
}
func (s *Store) GetFlag(id string) *domain.QualityFlag {
	for _, list := range s.flags {
		for _, f := range list {
			if f.ID == id {
				return f
			}
		}
	}
	return nil
}

// ---- 批次与拒收 ----

func (s *Store) PutBatch(b *domain.Batch) { s.batches[b.ID] = b }
func (s *Store) GetBatch(id string) (*domain.Batch, bool) {
	b, ok := s.batches[id]
	return b, ok
}

func (s *Store) PutRejection(r *domain.Rejection) {
	s.rejections[r.ID] = r
	s.rejectionsByOrg[r.OrgID] = append(s.rejectionsByOrg[r.OrgID], r.ID)
	s.rejectionsByDevice[r.DeviceSerial] = append(s.rejectionsByDevice[r.DeviceSerial], r.ID)
}
func (s *Store) GetRejection(id string) (*domain.Rejection, bool) {
	r, ok := s.rejections[id]
	return r, ok
}
func (s *Store) AckRejection(id string) bool {
	if r, ok := s.rejections[id]; ok {
		r.AckedByOrg = true
		return true
	}
	return false
}

// ListRejections 返回质量反馈收件箱：可按机构或设备过滤，空字符串表示不过滤。
func (s *Store) ListRejections(orgID, deviceSerial string) []*domain.Rejection {
	var ids []string
	switch {
	case orgID != "":
		ids = s.rejectionsByOrg[orgID]
	case deviceSerial != "":
		ids = s.rejectionsByDevice[deviceSerial]
	default:
		ids = make([]string, 0, len(s.rejections))
		for id := range s.rejections {
			ids = append(ids, id)
		}
	}
	out := make([]*domain.Rejection, 0, len(ids))
	for _, id := range ids {
		r := s.rejections[id]
		if deviceSerial != "" && orgID != "" && (r.DeviceSerial != deviceSerial || r.OrgID != orgID) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// ---- 切片与清单 ----

func (s *Store) PutSlice(r *domain.SliceRequest) { s.slices[r.ID] = r }
func (s *Store) GetSlice(id string) (*domain.SliceRequest, bool) {
	r, ok := s.slices[id]
	return r, ok
}
func (s *Store) ListSlices(orgID string) []*domain.SliceRequest {
	out := []*domain.SliceRequest{}
	for _, r := range s.slices {
		if orgID != "" && r.OrgID != orgID {
			continue
		}
		out = append(out, r)
	}
	return out
}

func (s *Store) PutManifest(m *domain.DatasetManifest) { s.manifests[m.ID] = m }
func (s *Store) GetManifest(id string) (*domain.DatasetManifest, bool) {
	m, ok := s.manifests[id]
	return m, ok
}
