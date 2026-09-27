package models

import "testing"

// TestResolveSubstringRanking kiểm chứng thứ tự xếp hạng mới của Resolve khi bí danh ngắn
// khớp nhiều bản ghi (trước đây lấy bản đầu tiên theo thứ tự catalog — tùy ý, hay trúng
// bản cũ nhất/đắt nhất). Bằng chứng thực nghiệm đã ghi trong docs/danh-gia-he-thong-viet-truyen.md §9.
func TestResolveSubstringRanking(t *testing.T) {
	r := NewModelRegistry()
	cases := []struct {
		pattern string
		wantID  string
	}{
		// wantID ghim theo catalog baseline hiện tại (models_generated.go) — khi
		// regenerate catalog, kỳ vọng này cần soát lại theo nguyên tắc "bản chuẩn mới nhất".
		// "gpt-4o" catalog không có bản trần; các bản dated dạng gạch phải chọn MỚI NHẤT.
		{"gpt-4o", "gpt-4o-2024-11-20"},
		// "claude-opus" trước đây trúng opus-4 ($75/1M, 200K); phải chọn bản chuẩn mới nhất
		// (opus-5.5 sau đợt regenerate 2026-09-27 — $20/1M, 1M cửa sổ).
		{"claude-opus", "claude-opus-5.5"},
		{"sonnet", "claude-sonnet-5"},
		// Đuôi chứa chữ (-fast) là model khác, không phải phiên bản — không được chọn
		// khi có bản thuần số cùng số hiệu.
		{"claude-sonnet-4", "claude-sonnet-4"},
	}
	for _, c := range cases {
		e, ok := r.Resolve(c.pattern)
		if !ok {
			t.Fatalf("Resolve(%q): không tìm thấy (kỳ vọng %s)", c.pattern, c.wantID)
		}
		if e.ID != c.wantID {
			t.Errorf("Resolve(%q) = %s (win=%d, out=$%.2f/1M), kỳ vọng %s", c.pattern, e.ID, e.ContextWindow, e.OutputCostPer1M, c.wantID)
		}
	}

	// Bí danh không khớp phần tử nào thì vẫn phải NOT FOUND chứ không đoán bừa.
	if _, ok := r.Resolve("gemini-flash"); ok {
		t.Error("Resolve(\"gemini-flash\") phải không tìm thấy (không ID nào chứa chuỗi này)")
	}
}

// TestSameModelIDMatrix chốt ngữ nghĩa SameModelID: hẹp theo hậu tố YYYYMMDD là CÓ CHỦ Ý —
// đuôi chữ (-preview/-exp) là model khác, tuyệtối không coi là cùng; dạng ngày gạch của
// OpenAI là false âm đã biết và chấp nhận (thận trọng hơn linh tinh trùng).
func TestSameModelIDMatrix(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"claude-sonnet-4", "claude-sonnet-4-20250514", true}, // hậu tố ngày YYYYMMDD
		{"claude-sonnet-4-20250514", "claude-sonnet-4", true}, // đối xứng
		{"claude-sonnet-4.6", "claude-sonnet-4-6", true},      // '.' ≡ '-'
		{"Claude-Sonnet-4", "claude-sonnet-4", true},          // không hoa thường
		{"gpt-4o", "gpt-4o-2024-08-06", false},                // dạng ngày gạch: false âm đã biết, chấp nhận
		{"gemini-2.5-pro", "gemini-2.5-pro-preview", false},   // -preview là model KHÁC
		{"deepseek-v3.2", "deepseek-v3.2-exp", false},         // -exp là model KHÁC
		{"claude-opus-4.8", "claude-opus-4.8-fast", false},    // -fast là model KHÁC
		{"gpt-4o", "gpt-4o-mini", false},                      // dòng khác hẳn
	}
	for _, c := range cases {
		if got := SameModelID(c.a, c.b); got != c.want {
			t.Errorf("SameModelID(%q, %q) = %v, kỳ vọng %v", c.a, c.b, got, c.want)
		}
	}
}
