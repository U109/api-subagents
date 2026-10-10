package config

import (
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// TestAdvancedOverrideRuntime 覆盖缺省兼容、开启与关闭，运行副本的默认值不得覆盖磁盘草稿或模型参数。
func TestAdvancedOverrideRuntime(t *testing.T) {
	for _, mode := range []string{"legacy", "on", "off"} {
		t.Run(mode, func(t *testing.T) {
			profile := shared.Object{"protocol": "responses", "model": "mock", "stream": false, "maxTokens": 8000, "firstResponseTimeoutSeconds": 300, "streamIdleTimeoutSeconds": 200, "taskTimeoutMinutes": 30, "reasoningEffort": "high", "modelContextWindows": shared.Object{"mock": 1000000}}
			if mode != "legacy" {
				profile["advancedOverride"] = mode == "on"
			}
			c, err := ValidateConfig(shared.Marshal(shared.Object{"version": 1, "models": shared.Object{"demo": profile}}), true)
			if err != nil {
				t.Fatal(err)
			}
			store := NewConfigStore(t.TempDir() + "/models.json")
			if err = store.Save(c); err != nil {
				t.Fatal(err)
			}
			c, err = store.Read()
			if err != nil {
				t.Fatal(err)
			}
			p, err := ResolveProfile(c, "demo")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "off" {
				if p.MaxTokens != 4096 || !p.Stream || p.FirstTimeout != 180 || p.IdleTimeout != 120 || p.TaskTimeout != 15 {
					t.Fatal("disabled override did not use runtime defaults", p)
				}
			} else if p.MaxTokens != 8000 || p.Stream || p.FirstTimeout != 300 || p.IdleTimeout != 200 || p.TaskTimeout != 30 {
				t.Fatal("legacy/on parameters changed", p)
			}
			if c.Models["demo"].MaxTokens != 8000 || c.Models["demo"].Stream || c.Models["demo"].ReasoningEffort != "high" || p.ModelContextWindows["mock"] != 1000000 {
				t.Fatal("raw values or independent model settings lost")
			}
		})
	}
}
