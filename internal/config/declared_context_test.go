package config

import (
	"github.com/U109/api-subagents/internal/shared"
	"testing"
)

// TestDeclaredCapacityCap 验证明确官方容量按 256K 取小且优先于旧手动设置，未知值不破坏旧配置。
func TestDeclaredCapacityCap(t *testing.T) {
	p := Profile{ModelContextWindows: map[string]int{"large": 900000, "small": 500000, "legacy": 500000}, ModelOfficialContexts: map[string]int{"small": 128000, "large": 1000000, "tiny": 2048}}
	for model, want := range map[string]int{"small": 128000, "large": 256000, "tiny": 2048, "unknown": 256000, "legacy": 500000} {
		if got := p.ContextWindow(model); got != want {
			t.Fatalf("%s got %d want %d", model, got, want)
		}
	}
}

// TestDeclaredCapacityNormalization 验证保存只保留已选模型容量并隔离编辑副本，不接受负数和超大数据。
func TestDeclaredCapacityNormalization(t *testing.T) {
	for _, capacity := range []int{-1, 100000001} {
		_, err := normalizeModelOfficialContexts(Profile{Model: "one", ModelOfficialContexts: map[string]int{"one": capacity}})
		if err == nil {
			t.Fatal("invalid capacity accepted")
		}
	}
	raw := shared.Object{"version": 1, "models": shared.Object{"demo": shared.Object{"protocol": "responses", "model": "one", "modelOfficialContexts": shared.Object{"one": 128000, "removed": 1000000}}}}
	c, err := ValidateConfig(shared.Marshal(raw), true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Models["demo"].ModelOfficialContexts["removed"] != 0 {
		t.Fatal("orphan metadata retained")
	}
	editable := Editable(c)
	editable.Models["demo"].ModelOfficialContexts["one"] = 64000
	if c.Models["demo"].ModelOfficialContexts["one"] != 128000 {
		t.Fatal("snapshot map shared")
	}
}
