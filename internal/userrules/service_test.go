package userrules

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
)

// Model nil + thư mục rules rỗng: chuẩn hóa hạ cấp toàn bộ, nhưng snapshot vẫn xuất được
// (system_defaults buộc phải) và ghi xuống đĩa. Hai thư mục của LoadOptions{} là chuỗi rỗng,
// RawFileSources trả nil, test không đụng đĩa thật.
func newDegradedService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st := store.NewStore(t.TempDir())
	return NewService(st, nil, rules.LoadOptions{}), st
}

func TestService_Build_DegradesButPersists(t *testing.T) {
	svc, st := newDegradedService(t)

	snap, err := svc.Build(t.Context(), "每章1200字，主角冷静克制")
	if err != nil {
		t.Fatalf("Build không được báo lỗi (hạ cấp thay vì chặn): %v", err)
	}
	if snap.Status != rules.StatusDegraded {
		t.Fatalf("Không có model phải hạ cấp, status=%q", snap.Status)
	}
	// system_defaults luôn là fallback cho cơ sở cơ học.
	if len(snap.Structured.FatigueWords) == 0 || len(snap.Structured.ForbiddenPhrases) == 0 {
		t.Fatalf("Phải giữ cơ sở cơ học của system_defaults, got %+v", snap.Structured)
	}
	// Prompt khởi động hạ cấp thành raw preferences, văn bản gốc không mất.
	if snap.Preferences == "" {
		t.Fatal("Hạ cấp phải ghi nguyên văn prompt khởi động vào preferences")
	}

	// Đã ghi xuống đĩa: GetOrBuild đọc lại cùng một bản thay vì dựng lại.
	reloaded, err := st.UserRules.Load()
	if err != nil || reloaded == nil {
		t.Fatalf("Snapshot phải đã ghi xuống đĩa: err=%v snap=%v", err, reloaded)
	}
	if reloaded.Preferences != snap.Preferences {
		t.Fatal("Nội dung ghi xuống đĩa khác giá trị trả về")
	}
}

func TestService_GetOrBuildInitializesMissingSnapshot(t *testing.T) {
	svc, st := newDegradedService(t)

	if cur, _ := st.UserRules.Load(); cur != nil {
		t.Fatal("Ban đầu phải không có snapshot")
	}
	snap, err := svc.GetOrBuild(t.Context())
	if err != nil {
		t.Fatalf("GetOrBuild không được báo lỗi: %v", err)
	}
	if len(snap.Structured.FatigueWords) == 0 {
		t.Fatal("Sinh lười phải chứa system_defaults")
	}
	if cur, _ := st.UserRules.Load(); cur == nil {
		t.Fatal("GetOrBuild phải ghi xuống đĩa luôn")
	}
}

func TestService_AddRuntimeRule_PersistsAndReturnsCandidate(t *testing.T) {
	svc, st := newDegradedService(t)

	const text = "以后少用比喻"
	merged, cand, err := svc.AddRuntimeRule(t.Context(), text)
	if err != nil {
		t.Fatalf("AddRuntimeRule không được báo lỗi: %v", err)
	}
	// Ứng viên dùng để hiển thị lại: không model thì hạ cấp, văn bản gốc vào preferences.
	if !cand.Degraded {
		t.Fatal("Không model thì ứng viên lần này phải hạ cấp")
	}
	if cand.Preferences != text {
		t.Fatalf("Ứng viên phải giữ nguyên văn, got %q", cand.Preferences)
	}
	// Sau khi phủ, snapshot chứa mục đó và đã ghi xuống đĩa.
	if merged.Preferences == "" {
		t.Fatal("Sau khi phủ preferences không được rỗng")
	}
	reloaded, err := st.UserRules.Load()
	if err != nil || reloaded == nil {
		t.Fatalf("Sau khi phủ phải ghi xuống đĩa: err=%v", err)
	}
	if reloaded.Status != rules.StatusDegraded {
		t.Fatalf("Có nguồn hạ cấp, status phải là degraded, got %q", reloaded.Status)
	}
}
