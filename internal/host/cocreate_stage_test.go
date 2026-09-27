package host

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/store"
)

// newFlagTestHost dựng một Host tối tiểu, vừa đủ dẫn động máy trạng thái cờ cocreating và guard đồng thời.
// emitEvent dùng kênh không khóa, buffer events là đủ, không cần observer.
// Nhánh trạng thái chạy của PauseForCoCreate sẽ gọi Engine Abort (tái dùng đường tạm dừng Esc đã kiểm chứng),
// không phủ trong unit test này; ở đây chỉ phủ trạng thái không chạy và logic cờ/guard.
func newFlagTestHost(lc lifecycle, cocreating bool) *Host {
	return &Host{
		lifecycle:  lc,
		cocreating: cocreating,
		engine:     &engine{}, // acquireExclusive kiểm tra engine.isRunning() (cổng kiểm soát cửa sổ dừng)
		events:     make(chan Event, 16),
	}
}

func TestPauseForCoCreate_NonRunningSetsFlag(t *testing.T) {
	h := newFlagTestHost(lifecycleIdle, false)
	if !h.PauseForCoCreate() {
		t.Fatal("Trạng thái idle phải cho vào đồng sáng tạo theo giai đoạn")
	}
	if !h.cocreating {
		t.Error("Sau khi vào cocreating phải là true")
	}
	if h.lifecycle != lifecycleIdle {
		t.Errorf("Vào từ trạng thái không chạy không được đổi lifecycle, được %s", h.lifecycle)
	}
}

func TestPauseForCoCreate_RejectsCompleted(t *testing.T) {
	h := newFlagTestHost(lifecycleCompleted, false)
	if h.PauseForCoCreate() {
		t.Error("Sau khi toàn sách hoàn thành không được cho vào đồng sáng tạo theo giai đoạn")
	}
	if h.cocreating {
		t.Error("Sau khi từ chối không được đặt cờ cocreating")
	}
}

func TestPauseForCoCreate_RejectsReentrant(t *testing.T) {
	h := newFlagTestHost(lifecyclePaused, true)
	if h.PauseForCoCreate() {
		t.Error("Đang trong đồng sáng tạo phải từ chối vào lại")
	}
}

func TestCancelCoCreate_ClearsFlag(t *testing.T) {
	h := newFlagTestHost(lifecyclePaused, true)
	h.CancelCoCreate()
	if h.cocreating {
		t.Error("Sau khi hủy cocreating phải được dọn sạch")
	}
	if h.lifecycle != lifecyclePaused {
		t.Errorf("Hủy không được đổi lifecycle, được %s", h.lifecycle)
	}
}

func TestCancelCoCreate_NoopWhenNotCocreating(t *testing.T) {
	h := newFlagTestHost(lifecycleRunning, false)
	h.CancelCoCreate() // không được panic, không được đổi trạng thái
	if h.cocreating || h.lifecycle != lifecycleRunning {
		t.Error("Trạng thái không đồng sáng tạo CancelCoCreate phải là no-op")
	}
}

func TestResumeFromCoCreate_RejectsEmptyDraft(t *testing.T) {
	h := newFlagTestHost(lifecyclePaused, true)
	if err := h.ResumeFromCoCreate("   "); err == nil {
		t.Fatal("Draft rỗng phải báo lỗi")
	}
	if !h.cocreating {
		t.Error("Draft rỗng trả về trước khi dọn cờ, cocreating phải giữ true")
	}
}

func TestResumeFromCoCreate_RejectsWhenNotCocreating(t *testing.T) {
	h := newFlagTestHost(lifecyclePaused, false)
	err := h.ResumeFromCoCreate("## 后续走向\n- 进入第二卷")
	if err == nil || !strings.Contains(err.Error(), "not in co-create") {
		t.Fatalf("Trạng thái không đồng sáng tạo phải báo not in co-create, được %v", err)
	}
}

func TestAcquireExclusive(t *testing.T) {
	cases := []struct {
		name       string
		lc         lifecycle
		cocreating bool
		exclusive  string
		wantErr    string // rỗng = kỳ vọng cho qua
	}{
		{"running", lifecycleRunning, false, "", "đang chạy"},
		{"cocreating", lifecyclePaused, true, "", "Đồng sáng tạo"},
		{"busy", lifecycleIdle, false, "nhập truyện", "đang diễn ra"},
		{"idle free", lifecycleIdle, false, "", ""},
		{"paused free", lifecyclePaused, false, "", ""},
	}
	// Cửa sổ dừng của Abort: lifecycle đã đặt paused nhưng goroutine engine chưa thoát hết, vẫn phải từ chối —
	// nếu không việc nhập sẽ ghi cùng một store đồng thời với phần dọn của engine.
	drain := newFlagTestHost(lifecyclePaused, false)
	drain.engine.running = true
	if err := drain.acquireExclusive("nhập truyện"); err == nil {
		t.Fatal("Giai đoạn engine đang drain phải từ chối tác vụ độc chiếm")
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newFlagTestHost(c.lc, c.cocreating)
			h.exclusive = c.exclusive
			err := h.acquireExclusive("nhập truyện")
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("Phải cho qua, được %v", err)
				}
				if h.exclusive != "nhập truyện" {
					t.Fatalf("Sau khi cho qua phải đăng ký chiếm dụng, được %q", h.exclusive)
				}
				h.releaseExclusive()
				if h.exclusive != "" {
					t.Fatalf("Sau khi nhả chiếm dụng phải dọn sạch, được %q", h.exclusive)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("Phải chứa %q, được %v", c.wantErr, err)
			}
			if !strings.Contains(err.Error(), "nhập truyện") {
				t.Errorf("Văn bản lỗi phải kèm action %q, được %v", "nhập truyện", err)
			}
		})
	}
}

// TestExclusiveBlocksCreationEntries canh #2: khi tác vụ độc chiếm nền (nhập/mô phỏng văn phong) đang chạy,
// không chỉ tác vụ nền thứ hai bị chặn, lối vào ghi sáng tác (Continue/Resume) và tác vụ nền mới cũng phải bị chặn,
// nếu không Continue sẽ để Arbiter đổi trạng thái trước khi engine bị cổng chặn, và trong lúc Resume/next engine có thể chạy trước.
func TestExclusiveBlocksCreationEntries(t *testing.T) {
	h := newFlagTestHost(lifecycleIdle, false)
	h.exclusive = "nhập truyện"
	if _, err := h.ImportFrom(context.Background(), imp.Options{}); err == nil {
		t.Error("Trong lúc tác vụ độc chiếm chạy ImportFrom phải bị từ chối")
	}
	if err := h.Continue("继续写"); err == nil {
		t.Error("Trong lúc tác vụ độc chiếm chạy Continue phải bị từ chối (phải chặn trước khi Arbiter phán định)")
	}
	if _, err := h.Resume(); err == nil {
		t.Error("Trong lúc tác vụ độc chiếm chạy Resume phải bị từ chối")
	}
}

// TestStageCoCreate_OccupancyBlocksConcurrentEntries kiểm chứng mọi lối vào độc chiếm đều bị chặn trong cửa sổ đồng sáng tạo:
// import/start/resume/continue đều phải bị từ chối trong lúc cocreating, bù khuyết điểm thời kỳ paused chỉ kiểm ==running.
func TestStageCoCreate_OccupancyBlocksConcurrentEntries(t *testing.T) {
	h := newFlagTestHost(lifecycleIdle, false)
	if !h.PauseForCoCreate() {
		t.Fatal("Vào đồng sáng tạo theo giai đoạn thất bại")
	}

	if _, err := h.ImportFrom(context.Background(), imp.Options{}); err == nil {
		t.Error("Trong cửa sổ đồng sáng tạo ImportFrom phải bị từ chối")
	}
	if err := h.StartPrepared("写个新故事"); err == nil {
		t.Error("Trong cửa sổ đồng sáng tạo StartPrepared phải bị từ chối")
	}
	if _, err := h.Resume(); err == nil {
		t.Error("Trong cửa sổ đồng sáng tạo Resume phải bị từ chối")
	}
	if err := h.Continue("继续写"); err == nil {
		t.Error("Trong cửa sổ đồng sáng tạo Continue phải bị từ chối")
	}

	// Sau khi thoát đồng sáng tạo chiếm dụng được giải trừ (ở đây đi Cancel; đường can thiệp Resume thuộc kiểm chứng tích hợp)
	h.CancelCoCreate()
	if h.cocreating {
		t.Fatal("Sau khi thoát cờ chiếm dụng phải được giải trừ")
	}
}

func TestBuildStoryStateSummary_NilStore(t *testing.T) {
	if got := buildStoryStateSummary(nil); got != "" {
		t.Errorf("Store nil phải trả chuỗi rỗng, được %q", got)
	}
}

func TestBuildStoryStateSummary_Populated(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(100); err != nil {
		t.Fatal(err)
	}
	if err := st.Book.Save(domain.BookMetadata{Title: "影之诗", Synopsis: "少年追索失落的影子。"}); err != nil {
		t.Fatal(err)
	}
	p, _ := st.Progress.Load()
	p.CompletedChapters = []int{1, 2, 3}
	p.TotalWordCount = 12000
	if err := st.Progress.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveCompass(domain.StoryCompass{
		EndingDirection: "主角登临绝巅",
		OpenThreads:     []string{"师门血仇未报"},
		EstimatedScale:  "预计 4-6 卷",
	}); err != nil {
		t.Fatal(err)
	}

	got := buildStoryStateSummary(st)
	for _, want := range []string{"影之诗", "đã hoàn thành 3 chương", "chương tiếp theo là chương 4", "主角登临绝巅", "师门血仇未报", "预计 4-6 卷"} {
		if !strings.Contains(got, want) {
			t.Errorf("Tóm tắt phải chứa %q, thực tế:\n%s", want, got)
		}
	}
}

func TestBuildStoryStateSummaryUsesDynamicPlanningWording(t *testing.T) {
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.Init(66); err != nil {
		t.Fatal(err)
	}
	if err := st.Outline.SaveLayeredOutline([]domain.VolumeOutline{{
		Index: 1, Title: "卷一", Arcs: []domain.ArcOutline{
			{Index: 1, Chapters: []domain.OutlineEntry{{Title: "一"}, {Title: "二"}}},
			{Index: 2, EstimatedChapters: 64},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	p, err := st.Progress.Load()
	if err != nil {
		t.Fatal(err)
	}
	p.Layered = true
	if err := st.Progress.Save(p); err != nil {
		t.Fatal(err)
	}

	got := buildStoryStateSummary(st)
	if !strings.Contains(got, "hiện đã chi tiết hóa 2 chương (phần sau lập kế hoạch động theo cung)") {
		t.Fatalf("Sai cách tính tóm tắt lập kế hoạch động:\n%s", got)
	}
	if strings.Contains(got, "66") || strings.Contains(got, "quy hoạch 2 chương") {
		t.Fatalf("Tóm tắt lập kế hoạch động không được ám chỉ tổng số chương cố định:\n%s", got)
	}
}
