package host

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// TestHostReopen canh lối mở lại cấp người dùng của /reopen: hoàn sách là quyết định lớn, mở lại chỉ do người dùng
// chủ động thực hiện — chưa hoàn thành thì từ chối, đang chạy thì từ chối; mở lại thành công đưa phase lùi về writing, hướng viết tiếp kèm theo ghi thành
// can thiệp treo (PendingSteer), lúc khôi phục đi qua Arbiter phán định rồi tiêm vào rồi mới chạy tiếp.
func TestHostReopen(t *testing.T) {
	st := storepkg.NewStore(t.TempDir())
	h := &Host{store: st, events: make(chan Event, 8)}

	if err := st.Progress.Init(2); err != nil {
		t.Fatal(err)
	}
	if err := h.Reopen(""); err == nil {
		t.Fatal("Sách chưa hoàn thành phải từ chối mở lại")
	}

	_ = st.Progress.UpdatePhase(domain.PhaseWriting)
	if err := st.Progress.MarkComplete(); err != nil {
		t.Fatal(err)
	}
	if err := h.Reopen("以八十年大限开新卷"); err != nil {
		t.Fatalf("Sách đã hoàn thành mở lại phải thành công: %v", err)
	}
	p, _ := st.Progress.Load()
	if p.Phase != domain.PhaseWriting {
		t.Fatalf("Sau khi mở lại phase phải là writing, được %s", p.Phase)
	}
	if len(p.PendingRewrites) != 0 || p.ReopenedFromComplete {
		t.Fatalf("Mở lại để viết tiếp không được mang ngữ nghĩa viết lại: %+v", p)
	}
	// Bộ đếm mở lại phải ghi đĩa: progress digest khi hoàn thành lần nữa mới khác lần trước — checkpoint cùng digest
	// khử trùng idempotent, hoàn thành lần nữa giống từng byte không có checkpoint mới, StopGuard sẽ phán đoán nhầm hoàn thành thành công là chấm dứt quay trống.
	if p.ReopenCount != 1 {
		t.Fatalf("Bộ đếm mở lại phải là 1, được %d", p.ReopenCount)
	}
	meta, _ := st.RunMeta.Load()
	if meta == nil || !strings.Contains(meta.PendingSteer, "八十年大限") {
		t.Fatalf("Hướng viết tiếp phải được ghi thành can thiệp treo, được %+v", meta)
	}

	running := &Host{store: st, lifecycle: lifecycleRunning, events: make(chan Event, 1)}
	if err := running.Reopen(""); err == nil {
		t.Fatal("Engine đang chạy phải từ chối mở lại")
	}
}
