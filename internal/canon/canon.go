// Package canon 提供稳定标识与指纹工具。
//
// 指纹用于把"当时生效的规则正文"固化为一个不可重释的摘要：
// 标准版本一经登记即冻结内容哈希，采集记录绑定的是哈希而非版本号，
// 后续即使同号修订（不允许）或新版本生效，也无法改变历史记录所依据的规则。
package canon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"strings"
)

// Hash 对任意 JSON 可序列化值计算规范摘要。
// 先以排序键的紧凑 JSON 规范化，再做 SHA-256，保证不同构造顺序得到同一指纹。
func Hash(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(normalize(decoded))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func normalize(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		// 简单插入排序，规则对象字段数量很少。
		for i := 1; i < len(keys); i++ {
			for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
				keys[j-1], keys[j] = keys[j], keys[j-1]
			}
		}
		ordered := make([][2]any, 0, len(keys))
		for _, key := range keys {
			ordered = append(ordered, [2]any{key, normalize(typed[key])})
		}
		return ordered
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = normalize(typed[i])
		}
		return out
	default:
		return value
	}
}

// DigestBytes 返回字节载荷的 "sha256:..." 摘要。
func DigestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Decimal 是以字符串保存的十进制精确量值，避免浮点误差进入育种数据。
// 比较与精度处理统一走 math/big.Rat。
type Decimal struct {
	value *big.Rat
	text  string
}

// ParseDecimal 解析形如 "12.30" 的十进制字符串。
func ParseDecimal(s string) (Decimal, error) {
	s = strings.TrimSpace(s)
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return Decimal{}, &ParseError{Text: s}
	}
	return Decimal{value: r, text: s}, nil
}

// MustDecimal 用于构造测试与常量，解析失败即 panic。
func MustDecimal(s string) Decimal {
	d, err := ParseDecimal(s)
	if err != nil {
		panic(err)
	}
	return d
}

// String 返回原始输入文本（保留尾随零体现声明精度）。
func (d Decimal) String() string { return d.text }

// Rat 暴露底层有理数以做比较。
func (d Decimal) Rat() *big.Rat {
	if d.value == nil {
		return new(big.Rat)
	}
	return new(big.Rat).Set(d.value)
}

// Cmp 返回 -1/0/1，与 big.Rat.Cmp 语义一致。
func (d Decimal) Cmp(other Decimal) int { return d.value.Cmp(other.value) }

// MarshalJSON 按字符串输出，杜绝中间浮点化。
func (d Decimal) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.text)
}

// UnmarshalJSON 只接受 JSON 字符串，拒绝 12.30 这种数字字面量。
func (d *Decimal) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return &ParseError{Text: string(data)}
	}
	parsed, err := ParseDecimal(text)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// IsZero 判断未初始化的零值。
func (d Decimal) IsZero() bool { return d.value == nil || d.text == "" }

// ParseError 表示十进制文本无法解析。
type ParseError struct{ Text string }

func (e *ParseError) Error() string { return "无法解析为十进制精确数值: " + e.Text }
