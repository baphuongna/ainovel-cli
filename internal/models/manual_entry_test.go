package models

import "testing"

// TestMergeModelsManualPin chốt ngữ nghĩa cờ Manual: entry do người dùng khai trong config
// là sự thật theo hợp đồng của họ — dữ liệu refresh từ OpenRouter không được đè; chỉ entry
// Manual mới cập nhật được một entry Manual (người dùng sửa config rồi restart vẫn thắng).
func TestMergeModelsManualPin(t *testing.T) {
	r := NewModelRegistry()
	// 1. Người dùng khai model proxy riêng kèm giá.
	r.MergeModels([]ModelEntry{
		{Provider: "custom-proxy", ID: "my-private-model", Manual: true, ContextWindow: 262144,
			InputCostPer1M: 0.5, OutputCostPer1M: 1.5},
	})
	e, ok := r.Resolve("custom-proxy/my-private-model")
	if !ok || e.OutputCostPer1M != 1.5 || e.ContextWindow != 262144 {
		t.Fatalf("sau merge Manual: got (%+v, %v)", e, ok)
	}

	// 2. Refresh nền về sau với dữ liệu nguồn cùng key — KHÔNG được đè giá/window Manual.
	r.MergeModels([]ModelEntry{
		{Provider: "custom-proxy", ID: "my-private-model", OutputCostPer1M: 99, ContextWindow: 4096},
	})
	e, _ = r.Resolve("custom-proxy/my-private-model")
	if e.OutputCostPer1M != 1.5 || e.ContextWindow != 262144 {
		t.Fatalf("refresh không được đè entry Manual: got %+v", e)
	}
	if e.Manual != true {
		t.Fatalf("cờ Manual phải giữ nguyên, got %v", e.Manual)
	}

	// 3. Người dùng cập nhật khai báo mới (Manual đè Manual) — thắng.
	r.MergeModels([]ModelEntry{
		{Provider: "custom-proxy", ID: "my-private-model", Manual: true, OutputCostPer1M: 2.0},
	})
	e, _ = r.Resolve("custom-proxy/my-private-model")
	if e.OutputCostPer1M != 2.0 {
		t.Fatalf("Manual mới phải đè Manual cũ: got %+v", e)
	}

	// 4. Entry thường (không Manual) vẫn bị refresh đè như cũ — hành vi gốc không đổi.
	r.MergeModels([]ModelEntry{{Provider: "x", ID: "normal-model", OutputCostPer1M: 3.0}})
	r.MergeModels([]ModelEntry{{Provider: "x", ID: "normal-model", OutputCostPer1M: 4.0}})
	e, _ = r.Resolve("x/normal-model")
	if e.OutputCostPer1M != 4.0 {
		t.Fatalf("entry thường phải theo dữ liệu nguồn mới nhất: got %+v", e)
	}
}
