package domain

import "time"

// UsageSchemaVersion là số phiên bản tương thích của meta/usage.json.
// Sau này nếu ngữ nghĩa trường của AgentUsageTotals thay đổi, tăng giá trị này; UsageStore.Load
// thấy phiên bản khác phải bỏ qua và kích hoạt replay dựng lại.
const UsageSchemaVersion = 2

// UsageState là snapshot lưu bền của mức dùng token / chi phí tích lũy.
// Trong bộ nhớ do UsageTracker duy trì, định kỳ debounce ghi xuống meta/usage.json.
//
// Lưu ý: sliding window samples nội bộ của UsageTracker ("tỉ lệ trúng N lần gần đây")
// **không lưu bền** — nó chỉ phục vụ chẩn đoán ngắn hạn của UI, tiến trình khởi động lại
// từ rỗng tích lũy lại vài vòng là phục hồi ngữ nghĩa. MissingAssistantUsage giữ lưu bền,
// tích lũy xuyên khởi động lại có giá trị chẩn đoán hơn.
type UsageState struct {
	Schema       int                         `json:"schema"`
	UpdatedAt    time.Time                   `json:"updated_at"`
	Overall      AgentUsageTotals            `json:"overall"`
	PerAgent     map[string]AgentUsageTotals `json:"per_agent"`
	PerModel     map[string]AgentUsageTotals `json:"per_model,omitempty"`
	MissingUsage int                         `json:"missing_assistant_usage"`
}

// AgentUsageTotals là hình thái lưu bền của bộ đếm tích lũy cho từng vai (hoặc overall).
type AgentUsageTotals struct {
	Input        int     `json:"input"`
	Output       int     `json:"output"`
	CacheRead    int     `json:"cache_read"`
	CacheWrite   int     `json:"cache_write"`
	Cost         float64 `json:"cost_usd"`
	Saved        float64 `json:"saved_usd"`
	CacheCapable bool    `json:"cache_capable"`
	// CacheBreaks là số lần chuỗi cache bị gãy do live phát hiện (tiền tố không ngắn đi mà
	// tỉ lệ trúng lao dốc). Chỉ tích lũy trên đường thời gian thực, session replay không phát
	// lại bước phát hiện này.
	CacheBreaks int `json:"cache_breaks,omitempty"`
}
