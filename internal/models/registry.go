// Package models cung cấp bảng đăng ký metadata các model LLM (cửa sổ ngữ cảnh, giới hạn
// đầu ra, giá), nguồn dữ liệu là OpenRouter API, baseline thời điểm biên dịch + làm mới lúc runtime.
package models

//go:generate go run gen_models.go

import (
	"strings"
	"sync"
)

// ModelEntry mô tả một model LLM đã biết.
type ModelEntry struct {
	Provider            string  `json:"provider"`               // Tên nhà cung cấp đã chuẩn hóa theo OpenRouter (anthropic/openai/gemini/...)
	ID                  string  `json:"id"`                     // ID model (không chứa tiền tố nhà cung cấp)
	Name                string  `json:"name"`                   // Tên hiển thị
	ContextWindow       int     `json:"context_window"`         // Cửa sổ nhập vào
	MaxTokens           int     `json:"max_tokens"`             // Giới hạn đầu ra mỗi lần
	InputCostPer1M      float64 `json:"input_cost_per_1m"`      // Giá nhập vào (USD/1M tokens)
	OutputCostPer1M     float64 `json:"output_cost_per_1m"`     // Giá đầu ra
	CacheReadCostPer1M  float64 `json:"cache_read_cost_per_1m"` // Giá đọc cache
	CacheWriteCostPer1M float64 `json:"cache_write_cost_per_1m"`
	// Manual đánh dấu entry do người dùng khai báo tường minh trong config (model ngoài
	// OpenRouter, proxy có biểu giá riêng): refresh nền KHÔNG được đè các trường của nó.
	Manual bool `json:"manual,omitempty"`
}

// ModelRegistry lưu các model đã biết, hỗ trợ phân giải mờ và gộp dữ liệu lúc runtime.
type ModelRegistry struct {
	mu     sync.RWMutex
	models []ModelEntry
}

// NewModelRegistry trả về bảng đăng ký đã nạp baseline thời điểm biên dịch.
func NewModelRegistry() *ModelRegistry {
	r := &ModelRegistry{}
	r.models = append(r.models, generatedModels...)
	return r
}

var (
	defaultRegistry     *ModelRegistry
	defaultRegistryOnce sync.Once
)

// DefaultRegistry trả về bảng đăng ký toàn cục (lazy-load, an toàn luồng).
// Ở giai đoạn khởi động, gọi StartPricingRefresh để nền làm mới giá/thông tin cửa sổ.
func DefaultRegistry() *ModelRegistry {
	defaultRegistryOnce.Do(func() {
		defaultRegistry = NewModelRegistry()
	})
	return defaultRegistry
}

// Resolve tra cứu bản ghi theo một định danh model (có thể là "provider/model", ID đầy đủ, hoặc tên cục bộ).
//
// Thứ tự khớp:
//  1. Nếu chứa "/", tra cứu chính xác theo "provider/model"
//  2. Khớp chính xác / theo hậu tố ngày
//  3. Khớp chuỗi con (ID hoặc Name chứa pattern)
//
// Khi khớp nhiều bản ghi, ưu tiên trả về bí danh không kèm hậu tố ngày
// (ví dụ claude-sonnet-4 được ưu tiên hơn claude-sonnet-4-20250514).
func (r *ModelRegistry) Resolve(pattern string) (*ModelEntry, bool) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	if idx := strings.Index(pattern, "/"); idx > 0 {
		prov := pattern[:idx]
		modelID := pattern[idx+1:]
		if entry, ok := lookupModelEntry(r.models, prov, modelID); ok {
			return &entry, true
		}
		// Tiền tố vendor của OpenRouter (google/, x-ai/) không nhất thiết trùng tên Provider cục bộ,
		// nên quay lại tra chỉ theo modelID, đảm bảo "google/gemini-2.5-pro" khớp được bản ghi gemini.
		if entry, ok := lookupModelEntry(r.models, "", modelID); ok {
			return &entry, true
		}
	}

	if entry, ok := lookupModelEntry(r.models, "", pattern); ok {
		return &entry, true
	}

	lower := strings.ToLower(pattern)
	normalized := normalizeModelLookupID(pattern)
	var candidates []int
	for i := range r.models {
		if strings.Contains(normalizeModelLookupID(r.models[i].ID), normalized) ||
			strings.Contains(strings.ToLower(r.models[i].ID), lower) ||
			strings.Contains(strings.ToLower(r.models[i].Name), lower) {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return nil, false
	}

	best := candidates[0]
	for _, i := range candidates[1:] {
		if substringResolveBetter(r.models[i], r.models[best], normalized) {
			best = i
		}
	}
	entry := r.models[best]
	return &entry, true
}

// ResolveContextWindow trả về cửa sổ ngữ cảnh của một model; không khớp thì trả về 0.
func (r *ModelRegistry) ResolveContextWindow(pattern string) int {
	if e, ok := r.Resolve(pattern); ok {
		return e.ContextWindow
	}
	return 0
}

// List trả về tất cả model (filter tùy chọn, chuỗi rỗng nghĩa là toàn bộ).
func (r *ModelRegistry) List(filter string) []ModelEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if filter == "" {
		return append([]ModelEntry{}, r.models...)
	}
	lower := strings.ToLower(filter)
	normalized := normalizeModelLookupID(filter)
	var out []ModelEntry
	for _, m := range r.models {
		if strings.Contains(strings.ToLower(m.Provider), lower) ||
			strings.Contains(normalizeModelLookupID(m.ID), normalized) ||
			strings.Contains(strings.ToLower(m.ID), lower) ||
			strings.Contains(strings.ToLower(m.Name), lower) {
			out = append(out, m)
		}
	}
	return out
}

// MergeModels gộp theo provider+id, không phân biệt hoa thường.
// Giá/cửa sổ/MaxTokens/Name khác 0 sẽ ghi đè bản ghi sẵn có; bản ghi mới được nối thẳng vào.
func (r *ModelRegistry) MergeModels(fetched []ModelEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()

	idx := make(map[string]int, len(r.models))
	for i, m := range r.models {
		idx[strings.ToLower(m.Provider+"/"+m.ID)] = i
	}
	for _, f := range fetched {
		key := strings.ToLower(f.Provider + "/" + f.ID)
		if i, ok := idx[key]; ok {
			existing := &r.models[i]
			// Entry do người dùng khai (Manual) là sự thật theo hợp đồng của họ — dữ liệu
			// nguồn (OpenRouter) không được đè; chỉ entry Manual mới cập nhật được Manual.
			if existing.Manual && !f.Manual {
				continue
			}
			if f.InputCostPer1M > 0 || f.OutputCostPer1M > 0 {
				existing.InputCostPer1M = f.InputCostPer1M
				existing.OutputCostPer1M = f.OutputCostPer1M
				existing.CacheReadCostPer1M = f.CacheReadCostPer1M
				existing.CacheWriteCostPer1M = f.CacheWriteCostPer1M
			}
			if f.ContextWindow > 0 {
				existing.ContextWindow = f.ContextWindow
			}
			if f.MaxTokens > 0 {
				existing.MaxTokens = f.MaxTokens
			}
			if f.Name != "" {
				existing.Name = f.Name
			}
			existing.Manual = existing.Manual || f.Manual
		} else {
			r.models = append(r.models, f)
			idx[key] = len(r.models) - 1
		}
	}
}
