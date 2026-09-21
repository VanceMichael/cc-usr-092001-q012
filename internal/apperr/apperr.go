// Package apperr 定义服务层的结构化错误码，HTTP 层据此映射状态码与拒收原因。
package apperr

import "fmt"

// 错误码词汇：全部为稳定字符串，可直接出现在拒收原因与跨机构反馈中。
const (
	NotFound        = "not_found"
	Conflict        = "conflict"
	Validation      = "validation_failed"
	Unauthorized    = "unauthorized"
	Forbidden       = "forbidden"
	LineageCycle    = "lineage_cycle"
	CalibrationBad  = "calibration_invalid"
	OutOfScope      = "device_out_of_scope"
	UnitMismatch    = "unit_mismatch"
	PrecisionBad    = "precision_exceeded"
	ValueOutOfRange = "value_out_of_range"
	WindowMissed    = "measurement_window_missed"
	ClockSkew       = "clock_skew"
	BatchLag        = "batch_lag_exceeded"
	DigestMismatch  = "digest_mismatch"
	UnknownSubject  = "unknown_subject"
	UncertaintyBad  = "uncertainty_exceeded"
	SliceState      = "slice_state_invalid"
)

// Error 携带机器可读错误码与面向操作员的中文说明。
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// New 构造一个错误。
func New(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}
