// Package api 装配网关 HTTP 路由。
//
// 鉴权约定（演示级实现）：
//   - 所有 /v1 业务请求必须带 X-Org-ID，表明操作机构；
//   - 联合体管理操作（标准登记、切片审批、证书吊销）另需
//     Authorization: Bearer <GATEWAY_TOKEN>；
//   - 机构只能操作本机构数据：属主校验在服务层与处理器层双重执行。
package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"example.com/batch-092001-q012/internal/apperr"
	"example.com/batch-092001-q012/internal/devices"
	"example.com/batch-092001-q012/internal/domain"
	"example.com/batch-092001-q012/internal/ingest"
	"example.com/batch-092001-q012/internal/provenance"
	"example.com/batch-092001-q012/internal/quality"
	"example.com/batch-092001-q012/internal/records"
	"example.com/batch-092001-q012/internal/service"
	"example.com/batch-092001-q012/internal/slices"
	"example.com/batch-092001-q012/internal/standards"
	"example.com/batch-092001-q012/internal/store"
	"example.com/batch-092001-q012/internal/subjects"
)

// Server 持有全部服务依赖。
type Server struct {
	Store      *store.Store
	Standards  *standards.Service
	Devices    *devices.Service
	Subjects   *subjects.Service
	Ingest     *ingest.Service
	Records    *records.Service
	Quality    *quality.Service
	Slices     *slices.Service
	Provenance *provenance.Service
	Now        func() time.Time

	gatewayToken string
	mux          *http.ServeMux
}

// NewServer 装配服务与路由。gatewayToken 为联合体管理令牌。
func NewServer(gatewayToken string, now func() time.Time) *Server {
	if now == nil {
		now = time.Now
	}
	s := &Server{
		Store:        store.New(),
		Now:          now,
		gatewayToken: gatewayToken,
	}
	s.Standards = standards.New(s.Store, now)
	s.Devices = devices.New(s.Store, now)
	s.Subjects = subjects.New(s.Store, now)
	s.Ingest = ingest.New(s.Store, s.Standards, s.Devices, s.Subjects, now)
	s.Records = records.New(s.Store, now)
	s.Quality = quality.New(s.Store, now)
	s.Slices = slices.New(s.Store, now)
	s.Provenance = provenance.New(s.Store, now)
	s.routes()
	return s
}

// Handler 暴露路由。
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	mux := http.NewServeMux()
	s.mux = mux

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, service.HealthPayload())
	})

	// 机构与标准（管理操作）
	mux.HandleFunc("POST /v1/orgs", s.admin(s.registerOrg))
	mux.HandleFunc("POST /v1/standards/{standard_id}/versions", s.admin(s.registerStandard))
	mux.HandleFunc("GET /v1/standards/{standard_id}/versions/{version}", s.auth(s.getStandard))
	mux.HandleFunc("GET /v1/standards/{standard_id}/effective", s.auth(s.effectiveStandard))
	mux.HandleFunc("POST /v1/standards/{standard_id}/versions/{version}/retire", s.admin(s.retireStandard))

	// 设备与校准
	mux.HandleFunc("POST /v1/devices", s.auth(s.registerDevice))
	mux.HandleFunc("GET /v1/devices/{serial}", s.auth(s.getDevice))
	mux.HandleFunc("POST /v1/devices/{serial}/calibrations", s.auth(s.addCalibration))
	mux.HandleFunc("POST /v1/calibrations/{id}/revoke", s.admin(s.revokeCalibration))

	// 个体、别名、谱系
	mux.HandleFunc("POST /v1/subjects", s.auth(s.registerSubject))
	mux.HandleFunc("GET /v1/subjects/{id}", s.auth(s.getSubject))
	mux.HandleFunc("GET /v1/subjects/{id}/lineage-history", s.auth(s.lineageHistory))
	mux.HandleFunc("POST /v1/subjects/merges", s.auth(s.mergeSubjects))
	mux.HandleFunc("POST /v1/subjects/lineage-corrections", s.auth(s.correctLineage))
	mux.HandleFunc("PUT /v1/orgs/{org_id}/aliases/{local_id}", s.auth(s.bindAlias))
	mux.HandleFunc("POST /v1/orgs/{org_id}/aliases/{local_id}/corrections", s.auth(s.correctAlias))

	// 采集
	mux.HandleFunc("POST /v1/ingest/batches", s.auth(s.ingestBatch))

	// 读数、修订、质量标记
	mux.HandleFunc("GET /v1/readings/{id}", s.auth(s.getReading))
	mux.HandleFunc("POST /v1/readings/{id}/revisions", s.auth(s.addRevision))
	mux.HandleFunc("POST /v1/readings/{id}/flags", s.auth(s.addFlag))
	mux.HandleFunc("POST /v1/flags/{id}/resolution", s.auth(s.resolveFlag))

	// 质量反馈
	mux.HandleFunc("GET /v1/quality/rejections", s.auth(s.listRejections))
	mux.HandleFunc("POST /v1/quality/rejections/{id}/ack", s.auth(s.ackRejection))

	// 切片
	mux.HandleFunc("POST /v1/slices", s.auth(s.applySlice))
	mux.HandleFunc("GET /v1/slices", s.auth(s.listSlices))
	mux.HandleFunc("GET /v1/slices/{id}", s.auth(s.getSlice))
	mux.HandleFunc("POST /v1/slices/{id}/review", s.admin(s.reviewSlice))
	mux.HandleFunc("POST /v1/slices/{id}/revoke", s.admin(s.revokeSlice))
	mux.HandleFunc("GET /v1/slices/{id}/export", s.auth(s.exportSlice))

	// 数据集溯源
	mux.HandleFunc("POST /v1/manifests", s.auth(s.buildManifest))
	mux.HandleFunc("GET /v1/manifests/{id}", s.auth(s.getManifest))
	mux.HandleFunc("POST /v1/manifests/{id}/verify", s.auth(s.verifyManifest))
}

// ---- 中间件 ----

type authedHandler func(w http.ResponseWriter, r *http.Request, orgID string)

func (s *Server) auth(next authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID := strings.TrimSpace(r.Header.Get("X-Org-ID"))
		if orgID == "" {
			writeError(w, apperr.New(apperr.Unauthorized, "缺少 X-Org-ID 请求头"))
			return
		}
		authed := false
		if err := s.Store.View(func() error {
			org, ok := s.Store.GetOrg(orgID)
			if ok && org.Active {
				authed = true
			}
			return nil
		}); err != nil {
			writeError(w, err)
			return
		}
		if !authed {
			writeError(w, apperr.New(apperr.Unauthorized, "机构 %s 未登记或已停用", orgID))
			return
		}
		next(w, r, orgID)
	}
}

func (s *Server) admin(next authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.gatewayToken == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error":             "admin_disabled",
				"error_description": "网关联合体管理令牌未配置",
			})
			return
		}
		token := bearerToken(r)
		if token != s.gatewayToken {
			writeError(w, apperr.New(apperr.Unauthorized, "管理操作需要有效的联合体令牌"))
			return
		}
		orgID := r.Header.Get("X-Org-ID")
		if orgID == "" {
			orgID = "CONSORTIUM"
		}
		next(w, r, orgID)
	}
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return ""
}

// ---- 机构 ----

type orgRequest struct {
	OrgID string `json:"org_id"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

func (s *Server) registerOrg(w http.ResponseWriter, r *http.Request, orgID string) {
	var in orgRequest
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if in.OrgID == "" || in.Name == "" {
		writeError(w, apperr.New(apperr.Validation, "org_id 与 name 不能为空"))
		return
	}
	if in.Role != "institute" && in.Role != "farm" {
		writeError(w, apperr.New(apperr.Validation, "role 只接受 institute/farm"))
		return
	}
	o := &domain.Org{
		ID:           in.OrgID,
		Name:         in.Name,
		Role:         in.Role,
		Active:       true,
		RegisteredAt: s.Now(),
	}
	if err := s.Store.Update(func() error {
		if _, exists := s.Store.GetOrg(in.OrgID); exists {
			return apperr.New(apperr.Conflict, "机构 %s 已登记", in.OrgID)
		}
		s.Store.PutOrg(o)
		return nil
	}); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, o)
}

// ---- 标准 ----

type standardRequest struct {
	Version     string         `json:"version"`
	EffectiveAt time.Time      `json:"effective_at"`
	Rules       domain.RuleSet `json:"rules"`
}

func (s *Server) registerStandard(w http.ResponseWriter, r *http.Request, orgID string) {
	var in standardRequest
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	v, err := s.Standards.Register(standards.RegisterInput{
		StandardID:   r.PathValue("standard_id"),
		Version:      in.Version,
		EffectiveAt:  in.EffectiveAt,
		Rules:        in.Rules,
		RegisteredBy: orgID,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) getStandard(w http.ResponseWriter, r *http.Request, orgID string) {
	v, err := s.Standards.Get(r.PathValue("standard_id"), r.PathValue("version"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) effectiveStandard(w http.ResponseWriter, r *http.Request, orgID string) {
	at := s.Now()
	if raw := r.URL.Query().Get("at"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, apperr.New(apperr.Validation, "at 必须为 RFC3339 时间: %v", err))
			return
		}
		at = t
	}
	v, err := s.Standards.EffectiveAt(r.PathValue("standard_id"), at)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) retireStandard(w http.ResponseWriter, r *http.Request, orgID string) {
	if err := s.Standards.Retire(r.PathValue("standard_id"), r.PathValue("version")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "retired"})
}

// ---- 设备 ----

type deviceRequest struct {
	Serial      string   `json:"serial"`
	Kind        string   `json:"kind"`
	VendorModel string   `json:"vendor_model"`
	Species     []string `json:"species_scope"`
	TraitCodes  []string `json:"trait_scope"`
}

func (s *Server) registerDevice(w http.ResponseWriter, r *http.Request, orgID string) {
	var in deviceRequest
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	d, err := s.Devices.Register(devices.RegisterInput{
		Serial:      in.Serial,
		Kind:        in.Kind,
		VendorModel: in.VendorModel,
		OwnerOrgID:  orgID,
		Species:     in.Species,
		TraitCodes:  in.TraitCodes,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) getDevice(w http.ResponseWriter, r *http.Request, orgID string) {
	d, err := s.Devices.GetDevice(r.PathValue("serial"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

type calibrationRequest struct {
	IssuedAt       time.Time         `json:"issued_at"`
	DueAt          time.Time         `json:"due_at"`
	ExpiresAt      time.Time         `json:"expires_at"`
	CertRef        string            `json:"cert_ref"`
	CertDigest     string            `json:"cert_digest"`
	IssuedBy       string            `json:"issued_by"`
	MaxUncertainty map[string]string `json:"max_uncertainty"`
}

func (s *Server) addCalibration(w http.ResponseWriter, r *http.Request, orgID string) {
	var in calibrationRequest
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	c, err := s.Devices.AddCalibration(devices.AddCalibrationInput{
		DeviceSerial:   r.PathValue("serial"),
		IssuedAt:       in.IssuedAt,
		DueAt:          in.DueAt,
		ExpiresAt:      in.ExpiresAt,
		CertRef:        in.CertRef,
		CertDigest:     in.CertDigest,
		IssuedBy:       in.IssuedBy,
		MaxUncertainty: in.MaxUncertainty,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) revokeCalibration(w http.ResponseWriter, r *http.Request, orgID string) {
	var body struct {
		Reason string `json:"reason"`
	}
	_ = decode(r, &body)
	if err := s.Devices.RevokeCalibration(r.PathValue("id"), body.Reason); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// ---- 个体 ----

type subjectRequest struct {
	ID         string     `json:"animal_id"`
	Species    string     `json:"species"`
	Dob        *time.Time `json:"dob"`
	SireID     string     `json:"sire_id"`
	DamID      string     `json:"dam_id"`
	Generation int        `json:"generation"`
	Sex        string     `json:"sex"`
}

func (s *Server) registerSubject(w http.ResponseWriter, r *http.Request, orgID string) {
	var in subjectRequest
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	a, err := s.Subjects.Register(subjects.RegisterInput{
		ID:         in.ID,
		Species:    in.Species,
		Dob:        in.Dob,
		SireID:     in.SireID,
		DamID:      in.DamID,
		Generation: in.Generation,
		Sex:        in.Sex,
		ByOrgID:    orgID,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) getSubject(w http.ResponseWriter, r *http.Request, orgID string) {
	a, err := s.Subjects.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) lineageHistory(w http.ResponseWriter, r *http.Request, orgID string) {
	events, err := s.Subjects.LineageHistory(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lineage_events": events})
}

type mergeRequest struct {
	FromID string `json:"from_animal_id"`
	IntoID string `json:"into_animal_id"`
	Reason string `json:"reason"`
}

func (s *Server) mergeSubjects(w http.ResponseWriter, r *http.Request, orgID string) {
	var in mergeRequest
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if err := s.Subjects.Merge(in.FromID, in.IntoID, in.Reason, orgID); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "merged", "survivor_id": in.IntoID})
}

type lineageCorrectionRequest struct {
	AnimalID string  `json:"animal_id"`
	SireID   *string `json:"sire_id"`
	DamID    *string `json:"dam_id"`
	Reason   string  `json:"reason"`
}

func (s *Server) correctLineage(w http.ResponseWriter, r *http.Request, orgID string) {
	var in lineageCorrectionRequest
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if err := s.Subjects.CorrectLineage(in.AnimalID, in.SireID, in.DamID, in.Reason, orgID); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "corrected"})
}

type aliasRequest struct {
	CanonicalID string `json:"canonical_id"`
}

func (s *Server) bindAlias(w http.ResponseWriter, r *http.Request, orgID string) {
	pathOrg := r.PathValue("org_id")
	if pathOrg != orgID {
		writeError(w, apperr.New(apperr.Forbidden, "只能为本机构编号绑定别名"))
		return
	}
	var in aliasRequest
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if err := s.Subjects.BindAlias(pathOrg, r.PathValue("local_id"), in.CanonicalID); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "bound"})
}

type aliasCorrectionRequest struct {
	TargetCanonicalID string `json:"target_canonical_id"`
	Reason            string `json:"reason"`
}

func (s *Server) correctAlias(w http.ResponseWriter, r *http.Request, orgID string) {
	pathOrg := r.PathValue("org_id")
	if pathOrg != orgID {
		writeError(w, apperr.New(apperr.Forbidden, "只能纠正本机构编号绑定"))
		return
	}
	var in aliasCorrectionRequest
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if err := s.Subjects.CorrectAlias(pathOrg, r.PathValue("local_id"), in.TargetCanonicalID, in.Reason); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "corrected"})
}

// ---- 采集 ----

func (s *Server) ingestBatch(w http.ResponseWriter, r *http.Request, orgID string) {
	var in ingest.BatchInput
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	res, err := s.Ingest.Ingest(orgID, in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---- 读数 ----

func (s *Server) getReading(w http.ResponseWriter, r *http.Request, orgID string) {
	view, err := s.Records.View(r.PathValue("id"), orgID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

type revisionRequest struct {
	NewValue string `json:"new_value"`
	Reason   string `json:"reason"`
}

func (s *Server) addRevision(w http.ResponseWriter, r *http.Request, orgID string) {
	var in revisionRequest
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	rev, err := s.Records.AddRevision(records.AddRevisionInput{
		ReadingID: r.PathValue("id"),
		NewValue:  in.NewValue,
		Reason:    in.Reason,
		ByOrgID:   orgID,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rev)
}

type flagRequest struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Detail   string `json:"detail"`
}

func (s *Server) addFlag(w http.ResponseWriter, r *http.Request, orgID string) {
	var in flagRequest
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	f, err := s.Records.AddFlag(records.AddFlagInput{
		ReadingID: r.PathValue("id"),
		Severity:  in.Severity,
		Code:      in.Code,
		Detail:    in.Detail,
		ByOrgID:   orgID,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

type flagResolutionRequest struct {
	Resolution string `json:"resolution"`
	Reject     bool   `json:"reject"`
}

func (s *Server) resolveFlag(w http.ResponseWriter, r *http.Request, orgID string) {
	var in flagResolutionRequest
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	f, err := s.Records.ResolveFlag(r.PathValue("id"), orgID, in.Resolution, in.Reject)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// ---- 质量反馈 ----

func (s *Server) listRejections(w http.ResponseWriter, r *http.Request, orgID string) {
	device := r.URL.Query().Get("device_serial")
	if device != "" {
		list, err := s.Quality.DeviceFeedback(device)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"rejections": list})
		return
	}
	list, err := s.Quality.OrgInbox(orgID, r.URL.Query().Get("only_unacked") == "true")
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rejections": list})
}

func (s *Server) ackRejection(w http.ResponseWriter, r *http.Request, orgID string) {
	if err := s.Quality.Ack(orgID, r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "acked"})
}

// ---- 切片 ----

type sliceRequest struct {
	Purpose        string    `json:"purpose"`
	Species        []string  `json:"species"`
	TraitCodes     []string  `json:"trait_codes"`
	DateFrom       time.Time `json:"date_from"`
	DateTo         time.Time `json:"date_to"`
	IncludeLineage bool      `json:"include_lineage"`
	ValidDays      int       `json:"valid_days"`
}

func (s *Server) applySlice(w http.ResponseWriter, r *http.Request, orgID string) {
	var in sliceRequest
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	req, err := s.Slices.Apply(slices.ApplyInput{
		OrgID:          orgID,
		Purpose:        in.Purpose,
		Species:        in.Species,
		TraitCodes:     in.TraitCodes,
		DateFrom:       in.DateFrom,
		DateTo:         in.DateTo,
		IncludeLineage: in.IncludeLineage,
		ValidDays:      in.ValidDays,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, req)
}

func (s *Server) listSlices(w http.ResponseWriter, r *http.Request, orgID string) {
	writeJSON(w, http.StatusOK, map[string]any{"slice_requests": s.Slices.ListByOrg(orgID)})
}

func (s *Server) getSlice(w http.ResponseWriter, r *http.Request, orgID string) {
	req, err := s.Slices.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if req.OrgID != orgID {
		writeError(w, apperr.New(apperr.Forbidden, "切片申请不属于本机构"))
		return
	}
	writeJSON(w, http.StatusOK, req)
}

type sliceReviewRequest struct {
	Approve          bool      `json:"approve"`
	Note             string    `json:"note"`
	ApprovedSubjects []string  `json:"approved_subjects"`
	ExpiresAt        time.Time `json:"expires_at"`
}

func (s *Server) reviewSlice(w http.ResponseWriter, r *http.Request, orgID string) {
	var in sliceReviewRequest
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	req, err := s.Slices.Review(r.PathValue("id"), orgID, in.Approve, in.Note, in.ApprovedSubjects, in.ExpiresAt)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (s *Server) revokeSlice(w http.ResponseWriter, r *http.Request, orgID string) {
	var body struct {
		Note string `json:"note"`
	}
	_ = decode(r, &body)
	if err := s.Slices.Revoke(r.PathValue("id"), orgID, body.Note); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// exportSlice 导出后立即为导出读数生成溯源清单，清单编号随导出返回。
func (s *Server) exportSlice(w http.ResponseWriter, r *http.Request, orgID string) {
	export, err := s.Slices.Export(orgID, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	ids := make([]string, 0, len(export.Records))
	for _, rec := range export.Records {
		ids = append(ids, rec.ReadingID)
	}
	if len(ids) > 0 {
		m, err := s.Provenance.BuildForSlice(provenance.BuildInput{
			CreatorOrg:  orgID,
			Description: "切片 " + export.RequestID + " 自动生成数据集清单",
			ReadingIDs:  ids,
			SliceReqID:  export.RequestID,
		})
		if err != nil {
			writeError(w, err)
			return
		}
		export.ManifestID = m.ID
	}
	writeJSON(w, http.StatusOK, export)
}

// ---- 溯源清单 ----

type manifestRequest struct {
	Description string   `json:"description"`
	ReadingIDs  []string `json:"reading_ids"`
}

func (s *Server) buildManifest(w http.ResponseWriter, r *http.Request, orgID string) {
	var in manifestRequest
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	m, err := s.Provenance.Build(provenance.BuildInput{
		CreatorOrg:  orgID,
		Description: in.Description,
		ReadingIDs:  in.ReadingIDs,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) getManifest(w http.ResponseWriter, r *http.Request, orgID string) {
	m, err := s.Provenance.Get(r.PathValue("id"), orgID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) verifyManifest(w http.ResponseWriter, r *http.Request, orgID string) {
	res, err := s.Provenance.Verify(r.PathValue("id"), orgID)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if !res.Intact {
		status = http.StatusConflict
	}
	writeJSON(w, status, res)
}

// ---- 辅助 ----

func decode(r *http.Request, target any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return apperr.New(apperr.Validation, "请求体无法解析: %v", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, err error) {
	if ae, ok := err.(*apperr.Error); ok {
		writeJSON(w, statusForCode(ae.Code), map[string]string{
			"error":             ae.Code,
			"error_description": ae.Message,
		})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{
		"error":             "internal_error",
		"error_description": err.Error(),
	})
}

func statusForCode(code string) int {
	switch code {
	case apperr.NotFound:
		return http.StatusNotFound
	case apperr.Conflict, apperr.SliceState:
		return http.StatusConflict
	case apperr.Unauthorized:
		return http.StatusUnauthorized
	case apperr.Forbidden:
		return http.StatusForbidden
	default:
		return http.StatusUnprocessableEntity
	}
}
