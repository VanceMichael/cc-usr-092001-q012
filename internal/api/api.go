// Package api 提供接入网关的 HTTP 接口。
package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"example.com/batch-092001-q012/internal/domain"
	"example.com/batch-092001-q012/internal/service"
)

// Handler 把业务服务暴露为 REST 接口。
type Handler struct {
	svc *service.Service
}

// NewMux 装配全部路由。
func NewMux(svc *service.Service) http.Handler {
	h := &Handler{svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/standards", h.registerStandard)
	mux.HandleFunc("POST /v1/standards/{id}/activate", h.activateStandard)
	mux.HandleFunc("POST /v1/standards/{id}/retire", h.retireStandard)
	mux.HandleFunc("POST /v1/standards/{id}/species", h.registerSpecies)
	mux.HandleFunc("POST /v1/standards/{id}/traits", h.registerTrait)
	mux.HandleFunc("POST /v1/standards/{id}/protocols", h.registerProtocol)
	mux.HandleFunc("POST /v1/standards/{id}/formats", h.registerFormat)
	mux.HandleFunc("POST /v1/devices", h.registerDevice)
	mux.HandleFunc("POST /v1/devices/{serial}/calibrations", h.registerCalibration)
	mux.HandleFunc("POST /v1/subjects", h.registerSubject)
	mux.HandleFunc("POST /v1/subjects/merge", h.mergeSubjects)
	mux.HandleFunc("GET /v1/subjects/{ref}/pedigree", h.pedigree)
	mux.HandleFunc("POST /v1/uploads", h.ingestBatch)
	mux.HandleFunc("GET /v1/observations/{id}", h.getObservation)
	mux.HandleFunc("POST /v1/observations/{id}/revisions", h.addRevision)
	mux.HandleFunc("POST /v1/observations/{id}/quality-marks", h.addQualityMark)
	mux.HandleFunc("POST /v1/observations/{id}/feedback", h.addFeedback)
	mux.HandleFunc("GET /v1/observations/{id}/feedback", h.feedbackFor)
	mux.HandleFunc("GET /v1/orgs/{org}/rejections", h.rejectionsForOrg)
	mux.HandleFunc("POST /v1/slice-requests", h.requestSlice)
	mux.HandleFunc("POST /v1/slice-requests/{id}/decision", h.decideSlice)
	mux.HandleFunc("GET /v1/slice-requests/{id}/data", h.sliceData)
	mux.HandleFunc("POST /v1/datasets", h.createDataset)
	mux.HandleFunc("GET /v1/datasets/{id}/provenance", h.provenance)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return false
	}
	return true
}

// respond 把业务错误映射为 HTTP 状态码。
func respond(w http.ResponseWriter, value any, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, value)
		return
	}
	var svcErr *service.Error
	if errors.As(err, &svcErr) {
		status := http.StatusBadRequest
		switch svcErr.Code {
		case "NOT_FOUND":
			status = http.StatusNotFound
		case "CONFLICT":
			status = http.StatusConflict
		case "PERMISSION_DENIED":
			status = http.StatusForbidden
		}
		writeJSON(w, status, svcErr)
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

func (h *Handler) registerStandard(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code          string `json:"code"`
		Version       string `json:"version"`
		EffectiveFrom string `json:"effective_from"`
	}
	if !decode(w, r, &body) {
		return
	}
	effectiveFrom, err := domain.ParseTime(body.EffectiveFrom)
	if err != nil {
		respond(w, nil, errInvalid(err.Error()))
		return
	}
	standard, err := h.svc.RegisterStandard(body.Code, body.Version, effectiveFrom)
	respond(w, standard, err)
}

func (h *Handler) activateStandard(w http.ResponseWriter, r *http.Request) {
	standard, err := h.svc.ActivateStandard(r.PathValue("id"))
	respond(w, standard, err)
}

func (h *Handler) retireStandard(w http.ResponseWriter, r *http.Request) {
	var body struct {
		At string `json:"at"`
	}
	if !decode(w, r, &body) {
		return
	}
	at, err := domain.ParseTime(body.At)
	if err != nil {
		respond(w, nil, errInvalid(err.Error()))
		return
	}
	standard, err := h.svc.RetireStandard(r.PathValue("id"), at)
	respond(w, standard, err)
}

func (h *Handler) registerSpecies(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	species, err := h.svc.RegisterSpecies(r.PathValue("id"), body.Code, body.Name)
	respond(w, species, err)
}

func (h *Handler) registerTrait(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SpeciesCode string   `json:"species_code"`
		Code        string   `json:"code"`
		Name        string   `json:"name"`
		Unit        string   `json:"unit"`
		Precision   int      `json:"precision"`
		MinValue    *float64 `json:"min_value"`
		MaxValue    *float64 `json:"max_value"`
	}
	if !decode(w, r, &body) {
		return
	}
	trait, err := h.svc.RegisterTrait(r.PathValue("id"), body.SpeciesCode, body.Code, body.Name, body.Unit, body.Precision, body.MinValue, body.MaxValue)
	respond(w, trait, err)
}

func (h *Handler) registerProtocol(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SpeciesCode string `json:"species_code"`
		TraitCode   string `json:"trait_code"`
		MinAgeDays  int    `json:"min_age_days"`
		MaxAgeDays  int    `json:"max_age_days"`
	}
	if !decode(w, r, &body) {
		return
	}
	protocol, err := h.svc.RegisterProtocol(r.PathValue("id"), body.SpeciesCode, body.TraitCode, body.MinAgeDays, body.MaxAgeDays)
	respond(w, protocol, err)
}

func (h *Handler) registerFormat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code        string `json:"code"`
		Version     string `json:"version"`
		Description string `json:"description"`
	}
	if !decode(w, r, &body) {
		return
	}
	format, err := h.svc.RegisterFormat(r.PathValue("id"), body.Code, body.Version, body.Description)
	respond(w, format, err)
}

func (h *Handler) registerDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SerialNo     string   `json:"serial_no"`
		Model        string   `json:"model"`
		OrgRef       string   `json:"org_ref"`
		SpeciesCodes []string `json:"species_codes"`
		TraitCodes   []string `json:"trait_codes"`
	}
	if !decode(w, r, &body) {
		return
	}
	device, err := h.svc.RegisterDevice(body.SerialNo, body.Model, body.OrgRef, body.SpeciesCodes, body.TraitCodes)
	respond(w, device, err)
}

func (h *Handler) registerCalibration(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CertNo     string `json:"cert_no"`
		IssuedBy   string `json:"issued_by"`
		ValidFrom  string `json:"valid_from"`
		ValidUntil string `json:"valid_until"`
		Digest     string `json:"digest"`
	}
	if !decode(w, r, &body) {
		return
	}
	validFrom, errFrom := domain.ParseTime(body.ValidFrom)
	validUntil, errUntil := domain.ParseTime(body.ValidUntil)
	if errFrom != nil || errUntil != nil {
		respond(w, nil, errInvalid("证书有效期时间格式不合法"))
		return
	}
	cert, err := h.svc.RegisterCalibration(r.PathValue("serial"), body.CertNo, body.IssuedBy, validFrom, validUntil, body.Digest)
	respond(w, cert, err)
}

func (h *Handler) registerSubject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref         string `json:"ref"`
		SpeciesCode string `json:"species_code"`
		OrgRef      string `json:"org_ref"`
		BirthDate   string `json:"birth_date"`
		SireRef     string `json:"sire_ref"`
		DamRef      string `json:"dam_ref"`
		Generation  int    `json:"generation"`
	}
	if !decode(w, r, &body) {
		return
	}
	subject, err := h.svc.RegisterSubject(body.Ref, body.SpeciesCode, body.OrgRef, body.BirthDate, body.SireRef, body.DamRef, body.Generation)
	respond(w, subject, err)
}

func (h *Handler) mergeSubjects(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FromRef string `json:"from_ref"`
		ToRef   string `json:"to_ref"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	merge, err := h.svc.MergeSubjects(body.FromRef, body.ToRef, body.Reason)
	respond(w, merge, err)
}

func (h *Handler) pedigree(w http.ResponseWriter, r *http.Request) {
	depth := 2
	if value := r.URL.Query().Get("depth"); value != "" {
		var parsed int
		if _, err := parseInt(value, &parsed); err == nil {
			depth = parsed
		}
	}
	node, err := h.svc.Pedigree(r.PathValue("ref"), depth)
	respond(w, node, err)
}

func (h *Handler) ingestBatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OrgRef       string               `json:"org_ref"`
		DeviceSerial string               `json:"device_serial"`
		Format       string               `json:"format"`
		Items        []service.UploadItem `json:"items"`
	}
	if !decode(w, r, &body) {
		return
	}
	result, err := h.svc.IngestBatch(body.OrgRef, body.DeviceSerial, body.Format, body.Items)
	respond(w, result, err)
}

func (h *Handler) getObservation(w http.ResponseWriter, r *http.Request) {
	observation, revisions, marks, err := h.svc.GetObservation(r.PathValue("id"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	respond(w, map[string]any{
		"observation":   observation,
		"current_value": service.CurrentValue(observation, revisions),
		"revisions":     revisions,
		"quality_marks": marks,
	}, nil)
}

func (h *Handler) addRevision(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Value     string `json:"value"`
		Reason    string `json:"reason"`
		RevisedBy string `json:"revised_by"`
	}
	if !decode(w, r, &body) {
		return
	}
	revision, err := h.svc.AddRevision(r.PathValue("id"), body.Value, body.Reason, body.RevisedBy)
	respond(w, revision, err)
}

func (h *Handler) addQualityMark(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Flag     string `json:"flag"`
		Note     string `json:"note"`
		MarkedBy string `json:"marked_by"`
	}
	if !decode(w, r, &body) {
		return
	}
	mark, err := h.svc.AddQualityMark(r.PathValue("id"), body.Flag, body.Note, body.MarkedBy)
	respond(w, mark, err)
}

func (h *Handler) addFeedback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FromOrg string `json:"from_org"`
		Code    string `json:"code"`
		Detail  string `json:"detail"`
	}
	if !decode(w, r, &body) {
		return
	}
	feedback, err := h.svc.AddFeedback(r.PathValue("id"), body.FromOrg, body.Code, body.Detail)
	respond(w, feedback, err)
}

func (h *Handler) feedbackFor(w http.ResponseWriter, r *http.Request) {
	feedback, err := h.svc.FeedbackFor(r.PathValue("id"))
	respond(w, feedback, err)
}

func (h *Handler) rejectionsForOrg(w http.ResponseWriter, r *http.Request) {
	respond(w, h.svc.RejectionsForOrg(r.PathValue("org")), nil)
}

func (h *Handler) requestSlice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OrgRef         string   `json:"org_ref"`
		Purpose        string   `json:"purpose"`
		SpeciesCode    string   `json:"species_code"`
		TraitCodes     []string `json:"trait_codes"`
		From           string   `json:"from"`
		To             string   `json:"to"`
		IncludeSubject bool     `json:"include_subject"`
	}
	if !decode(w, r, &body) {
		return
	}
	from, errFrom := domain.ParseTime(body.From)
	to, errTo := domain.ParseTime(body.To)
	if errFrom != nil || errTo != nil {
		respond(w, nil, errInvalid("切片时间范围格式不合法"))
		return
	}
	request, err := h.svc.RequestSlice(body.OrgRef, body.Purpose, body.SpeciesCode, body.TraitCodes, from, to, body.IncludeSubject)
	respond(w, request, err)
}

func (h *Handler) decideSlice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Approve   bool   `json:"approve"`
		DecidedBy string `json:"decided_by"`
	}
	if !decode(w, r, &body) {
		return
	}
	request, err := h.svc.DecideSlice(r.PathValue("id"), body.Approve, body.DecidedBy)
	respond(w, request, err)
}

func (h *Handler) sliceData(w http.ResponseWriter, r *http.Request) {
	records, err := h.svc.SliceData(r.PathValue("id"))
	respond(w, records, err)
}

func (h *Handler) createDataset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name           string   `json:"name"`
		OrgRef         string   `json:"org_ref"`
		ObservationIDs []string `json:"observation_ids"`
	}
	if !decode(w, r, &body) {
		return
	}
	dataset, err := h.svc.CreateDataset(body.Name, body.OrgRef, body.ObservationIDs)
	respond(w, dataset, err)
}

func (h *Handler) provenance(w http.ResponseWriter, r *http.Request) {
	report, err := h.svc.Provenance(r.PathValue("id"))
	respond(w, report, err)
}

func errInvalid(message string) error {
	return &service.Error{Code: "INVALID_ARGUMENT", Message: message}
}

func parseInt(text string, target *int) (bool, error) {
	var parsed int
	for _, r := range text {
		if r < '0' || r > '9' {
			return false, errors.New("不是非负整数")
		}
		parsed = parsed*10 + int(r-'0')
	}
	*target = parsed
	return true, nil
}
