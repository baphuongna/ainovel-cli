package bootstrap

import "testing"

// TestRolesAcceptThinkingRole chốt: "thinking" (model phỏng vấn đồng sáng tác) là role
// cấu hình được — trước đây cocreate.go gọi ForRole("thinking") nhưng knownRoles không
// có khóa này: user khai báo roles.thinking sẽ bị ValidateBase từ chối (role mồ côi).
func TestRolesAcceptThinkingRole(t *testing.T) {
	cfg := Config{
		Provider:  "anthropic",
		ModelName: "claude-sonnet-4.5",
		Providers: map[string]ProviderConfig{
			"anthropic": {APIKey: "sk-test-123"},
		},
		Roles: map[string]RoleConfig{
			"thinking": {Provider: "anthropic", Model: "claude-haiku-4.5"},
		},
	}
	if err := cfg.ValidateBase(); err != nil {
		t.Fatalf("roles.thinking phải được chấp nhận sau fix, được lỗi: %v", err)
	}
}

// TestRolesRejectUnknownRole.ensure guard cũ vẫn hoạt động cho tên role ngoài whitelist.
func TestRolesRejectUnknownRole(t *testing.T) {
	cfg := Config{
		Provider:  "anthropic",
		ModelName: "claude-sonnet-4.5",
		Providers: map[string]ProviderConfig{
			"anthropic": {APIKey: "sk-test-123"},
		},
		Roles: map[string]RoleConfig{
			"arbitrator": {Provider: "anthropic", Model: "claude-haiku-4.5"},
		},
	}
	if err := cfg.ValidateBase(); err == nil {
		t.Fatal("role lạ phải bị ValidateBase từ chối")
	}
}

// TestManualPriceEntries chốt việc dựng entry giá khai báo tay từ config: chỉ model có
// ít nhất một trường giá/window > 0 mới vào danh sách, entry mang cờ Manual.
func TestManualPriceEntries(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderConfig{
			"custom-proxy": {
				Models: []ModelConfig{
					{Name: "my-private-model", ContextWindow: 262144, InputCostPer1M: 0.5, OutputCostPer1M: 1.5, CacheReadCostPer1M: 0.05},
					{Name: "no-price-model"},
					{Name: "  "},
				},
			},
			"openai": {Models: []ModelConfig{{Name: "gpt-4o-mini"}}},
		},
	}
	entries := cfg.ManualPriceEntries()
	if len(entries) != 1 {
		t.Fatalf("chỉ 1 entry có khai báo giá/window, got %d: %+v", len(entries), entries)
	}
	e := entries[0]
	if e.Provider != "custom-proxy" || e.ID != "my-private-model" || !e.Manual {
		t.Fatalf("entry sai định danh/cờ: %+v", e)
	}
	if e.ContextWindow != 262144 || e.InputCostPer1M != 0.5 || e.OutputCostPer1M != 1.5 || e.CacheReadCostPer1M != 0.05 {
		t.Fatalf("trường giá sai: %+v", e)
	}
}

// TestManualPriceEntriesValidation: giá âm phải bị ValidateBase chặn.
func TestManualPriceEntriesValidation(t *testing.T) {
	cfg := Config{
		Provider:  "custom-proxy",
		ModelName: "my-private-model",
		Providers: map[string]ProviderConfig{
			"custom-proxy": {
				APIKey: "sk-test-123",
				Models: []ModelConfig{{Name: "my-private-model", OutputCostPer1M: -1}},
			},
		},
	}
	if err := cfg.ValidateBase(); err == nil {
		t.Fatal("giá âm phải bị ValidateBase từ chối")
	}
}
