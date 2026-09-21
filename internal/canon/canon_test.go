package canon_test

import (
	"encoding/json"
	"testing"

	"example.com/batch-092001-q012/internal/canon"
)

func TestHashIsKeyOrderIndependent(t *testing.T) {
	a := map[string]any{"unit": "kg", "value": "1.20", "nested": map[string]any{"x": 1, "y": 2}}
	b := map[string]any{"nested": map[string]any{"y": 2, "x": 1}, "value": "1.20", "unit": "kg"}
	ha, err := canon.Hash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := canon.Hash(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatal("规范哈希不应受键顺序影响")
	}
	if ha[:7] != "sha256:" {
		t.Fatal("哈希应有 sha256: 前缀")
	}
	c := map[string]any{"unit": "g", "value": "1.20"}
	hc, _ := canon.Hash(c)
	if hc == ha {
		t.Fatal("内容不同哈希应不同")
	}
}

func TestDecimalPrecision(t *testing.T) {
	d, err := canon.ParseDecimal("1.230")
	if err != nil {
		t.Fatal(err)
	}
	// 字符串保留尾随零（声明精度），比较按有理数值。
	if d.String() != "1.230" {
		t.Fatalf("应保留原始文本，实际 %s", d.String())
	}
	if d.Cmp(canon.MustDecimal("1.23")) != 0 {
		t.Fatal("1.230 与 1.23 数值应相等")
	}
	if d.Cmp(canon.MustDecimal("1.24")) >= 0 {
		t.Fatal("1.230 应小于 1.24")
	}
	if _, err := canon.ParseDecimal("abc"); err == nil {
		t.Fatal("非法十进制应报错")
	}
}

func TestDecimalRejectsJSONNumber(t *testing.T) {
	var d canon.Decimal
	// 只接受字符串，拒绝数字字面量，杜绝中间浮点化。
	if err := json.Unmarshal([]byte(`"1.5"`), &d); err != nil {
		t.Fatalf("字符串数字应可解析: %v", err)
	}
	if err := json.Unmarshal([]byte(`1.5`), &d); err == nil {
		t.Fatal("JSON 数字字面量必须拒绝")
	}
	raw, err := json.Marshal(canon.MustDecimal("2.00"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `"2.00"` {
		t.Fatalf("应序列化为字符串，实际 %s", raw)
	}
}
