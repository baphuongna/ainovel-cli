package models

import (
	"math"
	"testing"
	"time"
)

// ── convertModel：OpenRouter 条目 → ModelEntry ──

func TestConvertModel(t *testing.T) {
	fresh := time.Now().AddDate(0, 0, -30).Unix()
	cases := []struct {
		name  string
		m     openRouterModel
		ok    bool
		check func(t *testing.T, e ModelEntry)
	}{
		{
			name: "标准条目映射与计价换算",
			m: openRouterModel{
				ID: "deepseek/deepseek-chat", Name: "DeepSeek: V3", ContextLength: 64000,
				Created: fresh,
				Pricing: &openRouterPricing{
					Prompt: "0.0000015", Completion: "0.0000025",
					InputCacheRead: "0.0000001", InputCacheWrite: "0.0000015",
				},
				TopProvider: &openRouterTopProvider{MaxCompletionTokens: 8192},
			},
			ok: true,
			check: func(t *testing.T, e ModelEntry) {
				if e.Provider != "deepseek" || e.ID != "deepseek-chat" {
					t.Errorf("id 映射错误: %s/%s", e.Provider, e.ID)
				}
				if e.Name != "V3" { // "DeepSeek: V3" → 取 ": " 之后
					t.Errorf("cleanModelName 应剥厂商前缀, 得到 %q", e.Name)
				}
				if e.ContextWindow != 64000 || e.MaxTokens != 8192 {
					t.Errorf("窗口字段错误: %+v", e)
				}
				if e.InputCostPer1M != 1.5 || e.OutputCostPer1M != 2.5 {
					t.Errorf("主计价换算错误: in=%v out=%v", e.InputCostPer1M, e.OutputCostPer1M)
				}
				if e.CacheReadCostPer1M != 0.1 || e.CacheWriteCostPer1M != 1.5 {
					t.Errorf("缓存计价换算错误: read=%v write=%v", e.CacheReadCostPer1M, e.CacheWriteCostPer1M)
				}
			},
		},
		{
			name: "厂商前缀规范化（google→gemini）",
			m:    openRouterModel{ID: "google/gemini-2.5-flash", Name: "Google: Gemini 2.5 Flash", Created: fresh},
			ok:   true,
			check: func(t *testing.T, e ModelEntry) {
				if e.Provider != "gemini" {
					t.Errorf("google 应规范化为 gemini, 得到 %q", e.Provider)
				}
			},
		},
		{
			name: "无厂商前缀的条目剔除",
			m:    openRouterModel{ID: "no-slash", Created: fresh},
		},
		{
			name: "未知厂商剔除",
			m:    openRouterModel{ID: "unknown-vendor/model", Created: fresh},
		},
		{
			name: "变体后缀（:free）剔除",
			m:    openRouterModel{ID: "meta-llama/llama-3:free", Created: fresh},
		},
		{
			name: "过时模型剔除",
			m:    openRouterModel{ID: "openai/gpt-3.5-turbo", Created: time.Now().AddDate(-3, 0, 0).Unix()},
		},
		{
			name: "created 缺失按过时剔除",
			m:    openRouterModel{ID: "openai/gpt-x", Created: 0},
		},
		{
			name: "无 pricing 不炸、计价为 0",
			m:    openRouterModel{ID: "qwen/qwen-max", Created: fresh},
			ok:   true,
			check: func(t *testing.T, e ModelEntry) {
				if e.InputCostPer1M != 0 || e.OutputCostPer1M != 0 {
					t.Errorf("无 pricing 时计价应为 0: %+v", e)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, ok := convertModel(c.m)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if ok && c.check != nil {
				c.check(t, e)
			}
		})
	}
}

// ── tokenToMillion：字符串价格 → USD/1M ──

func TestTokenToMillion(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"", 0},
		{"0", 0},
		{"0.0000015", 1.5},
		{"0.0000025", 2.5},
		{"0.0000001", 0.1},
		{"1", 1000000},
		{"-0.5", 0}, // 负价按缺失处理
		{"abc", 0},  // 非数字按缺失处理
	}
	for _, c := range cases {
		if got := tokenToMillion(c.in); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("tokenToMillion(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// ── isStaleModel：730 天阈值 ──

func TestIsStaleModel(t *testing.T) {
	if !isStaleModel(0) || !isStaleModel(-1) {
		t.Error("缺失/负值 created 应视为过时")
	}
	if isStaleModel(time.Now().Unix()) {
		t.Error("刚发布的模型不应过时")
	}
	if isStaleModel(time.Now().AddDate(0, 0, -729).Unix()) {
		t.Error("729 天仍在阈值内")
	}
	if !isStaleModel(time.Now().AddDate(0, 0, -731).Unix()) {
		t.Error("731 天应过时")
	}
}

// ── cleanModelName ──

func TestCleanModelName(t *testing.T) {
	if got := cleanModelName("DeepSeek: V3"); got != "V3" {
		t.Errorf("应剥 \"厂商: \" 前缀, 得到 %q", got)
	}
	if got := cleanModelName("GPT-4o"); got != "GPT-4o" {
		t.Errorf("无前缀应原样保留, 得到 %q", got)
	}
}

// ── 模型查找：normalizeModelLookupID / modelLookupMatches / lookupModelEntry ──

func TestNormalizeModelLookupID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"GPT.4o-Mini", "gpt-4o-mini"},
		{"  Claude-3.7  ", "claude-3-7"},
	}
	for _, c := range cases {
		if got := normalizeModelLookupID(c.in); got != c.want {
			t.Errorf("normalizeModelLookupID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestModelLookupMatches(t *testing.T) {
	cases := []struct {
		known, target string
		want          bool
	}{
		{"claude-sonnet-4", "claude-sonnet-4", true},
		{"claude-sonnet-4", "claude-sonnet-4-20250514", true}, // 带日期后缀
		{"claude-sonnet-4-20250514", "claude-sonnet-4", true}, // 反向日期后缀
		{"claude-sonnet-4", "claude-sonnet-45", false},        // 非日期后缀
		{"claude-sonnet-4", "claude-sonnet-4-preview", false},
		{"gpt-4o", "gpt-4o-mini", false},
	}
	for _, c := range cases {
		if got := modelLookupMatches(c.known, c.target); got != c.want {
			t.Errorf("modelLookupMatches(%q, %q) = %v, want %v", c.known, c.target, got, c.want)
		}
	}
}

func TestLookupModelEntry(t *testing.T) {
	models := []ModelEntry{
		{Provider: "anthropic", ID: "claude-sonnet-4"},
		{Provider: "openai", ID: "gpt-4o"},
	}
	if e, ok := lookupModelEntry(models, "Anthropic", "Claude.Sonnet.4"); !ok || e.ID != "claude-sonnet-4" {
		t.Errorf("大小写/点号应归一匹配, got (%+v, %v)", e, ok)
	}
	if e, ok := lookupModelEntry(models, "openai", "gpt-4o-20240806"); !ok || e.ID != "gpt-4o" {
		t.Errorf("带日期后缀（-YYYYMMDD）应命中同 ID 条目, got (%+v, %v)", e, ok)
	}
	if _, ok := lookupModelEntry(models, "openai", "gpt-4o-preview"); ok {
		t.Error("非日期后缀不应命中")
	}
	if _, ok := lookupModelEntry(models, "openai", "claude-sonnet-4"); ok {
		t.Error("provider 不匹配时不应命中")
	}
}

// ── Registry：Resolve 模糊解析与 MergeModels 合并 ──

func TestRegistryResolveAndMerge(t *testing.T) {
	r := NewModelRegistry()
	r.MergeModels([]ModelEntry{{Provider: "deepseek", ID: "deepseek-chat", ContextWindow: 64000}})
	if e, ok := r.Resolve("deepseek/deepseek-chat"); !ok || e.ID != "deepseek-chat" {
		t.Fatalf("Resolve 精确匹配失败: (%+v, %v)", e, ok)
	}
	if _, ok := r.Resolve("nonexistent/model-x"); ok {
		t.Error("未知模型不应命中")
	}
	// MergeModels 不应清掉既有条目。
	r.MergeModels([]ModelEntry{{Provider: "openai", ID: "gpt-4o"}})
	if _, ok := r.Resolve("deepseek/deepseek-chat"); !ok {
		t.Error("合并新条目后旧条目应保留")
	}
}
