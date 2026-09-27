package host

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/flow"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type gateRecorder struct {
	paused  int
	reasons []string
}

func newAdvanceGateTest(t *testing.T, mode domain.ChapterAdvanceMode) (*storepkg.Store, *ChapterAdvanceGate, *gateRecorder) {
	t.Helper()
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("store init: %v", err)
	}
	if err := st.RunMeta.Init("default", "test", "test"); err != nil {
		t.Fatalf("run meta init: %v", err)
	}
	if err := st.RunMeta.SetAdvanceMode(mode); err != nil {
		t.Fatalf("advance mode: %v", err)
	}
	if err := st.Progress.Init(10); err != nil {
		t.Fatalf("progress init: %v", err)
	}
	if err := st.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		t.Fatalf("phase: %v", err)
	}
	recorder := &gateRecorder{}
	gate := NewChapterAdvanceGate(st, func(reason string) {
		recorder.paused++
		recorder.reasons = append(recorder.reasons, reason)
	}, func(_ string, summary string) {
		recorder.reasons = append(recorder.reasons, summary)
	})
	return st, gate, recorder
}

func TestChapterAdvanceGateReviewRequiresExactPermit(t *testing.T) {
	st, gate, recorder := newAdvanceGateTest(t, domain.ChapterAdvanceReview)
	forward := &flow.Instruction{Agent: "writer", Chapter: 1, Task: "写第 1 章"}

	allowed, err := gate.Allow(forward)
	if err != nil {
		t.Fatal(err)
	}
	if allowed || recorder.paused != 1 {
		t.Fatalf("Chương mới chưa được phép phải tạm dừng: allowed=%v paused=%d", allowed, recorder.paused)
	}
	if len(recorder.reasons) == 0 || !strings.Contains(recorder.reasons[len(recorder.reasons)-1], "/next") {
		t.Fatalf("Văn bản tạm dừng phải đưa ra cách cấp phép rõ ràng: %v", recorder.reasons)
	}

	if err := st.RunMeta.GrantAdvancePermit(1); err != nil {
		t.Fatal(err)
	}
	allowed, err = gate.Allow(forward)
	if err != nil || !allowed {
		t.Fatalf("Giấy phép khớp phải cho qua: allowed=%v err=%v", allowed, err)
	}
	if err := st.RunMeta.ClearAdvancePermit(1); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.GrantAdvancePermit(2); err != nil {
		t.Fatal(err)
	}
	allowed, err = gate.Allow(forward)
	if err == nil || allowed {
		t.Fatalf("Giấy phép không khớp phải thất bại rõ ràng: allowed=%v err=%v", allowed, err)
	}
}

func TestChapterAdvanceGateDoesNotGateRewriteOrRecovery(t *testing.T) {
	st, gate, _ := newAdvanceGateTest(t, domain.ChapterAdvanceReview)
	if err := st.Progress.MarkChapterComplete(1, 1000, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.SetPendingRewrites([]int{1}, "返工"); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.GrantAdvancePermit(2); err != nil {
		t.Fatal(err)
	}

	allowed, err := gate.Allow(&flow.Instruction{Agent: "writer", Chapter: 1, Task: "重写第 1 章"})
	if err != nil || !allowed {
		t.Fatalf("Viết lại không tiêu thụ giấy phép chương tiến lên: allowed=%v err=%v", allowed, err)
	}
	if gate.HandleBoundary() {
		t.Fatal("Khi có hàng đợi viết lại, giao thoa bình thường giữa permit và NextChapter không được báo hỏng nhầm")
	}
	meta, _ := st.RunMeta.Load()
	if meta.AdvancePermitChapter != 2 {
		t.Fatalf("Trong lúc viết lại giấy phép phải được giữ: %+v", meta)
	}

	if err := st.Signals.SavePendingCommit(domain.PendingCommit{Chapter: 2, Stage: domain.CommitStageStarted}); err != nil {
		t.Fatal(err)
	}
	allowed, err = gate.Allow(&flow.Instruction{Agent: "writer", Chapter: 2, Task: "恢复第 2 章提交"})
	if err != nil || !allowed {
		t.Fatalf("Khôi phục nộp không được coi là chương mới: allowed=%v err=%v", allowed, err)
	}
}

func TestChapterAdvanceGateConsumesPermitOnlyAfterStableCommit(t *testing.T) {
	st, gate, recorder := newAdvanceGateTest(t, domain.ChapterAdvanceReview)
	if err := st.RunMeta.GrantAdvancePermit(1); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.MarkChapterComplete(1, 1000, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.Signals.SavePendingCommit(domain.PendingCommit{Chapter: 1, Stage: domain.CommitStageProgressMarked}); err != nil {
		t.Fatal(err)
	}
	if gate.HandleBoundary() {
		t.Fatal("Khi saga nộp chưa xong không được tiêu thụ giấy phép hay dừng máy")
	}
	meta, _ := st.RunMeta.Load()
	if meta.AdvancePermitChapter != 1 {
		t.Fatalf("Trong lúc pending commit giấy phép phải được giữ: %+v", meta)
	}

	if err := st.Signals.ClearPendingCommit(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Checkpoints.Append(domain.ChapterScope(1), "commit", "", ""); err != nil {
		t.Fatal(err)
	}
	if gate.HandleBoundary() {
		t.Fatal("Nộp ổn định chỉ tiêu thụ giấy phép, tới trước lần phân phát kế mới vào chờ")
	}
	meta, _ = st.RunMeta.Load()
	if meta.AdvancePermitChapter != 0 {
		t.Fatalf("Sau nộp ổn định giấy phép phải được tiêu thụ: %+v", meta)
	}
	allowed, err := gate.Allow(&flow.Instruction{Agent: "writer", Chapter: 2})
	if err != nil || allowed || recorder.paused != 1 {
		t.Fatalf("Sau khi tiêu thụ, chương kế tiếp phải chờ cấp phép lại: allowed=%v paused=%d err=%v", allowed, recorder.paused, err)
	}
}

func TestChapterAdvanceGateRejectsCorruptPermitState(t *testing.T) {
	st, gate, recorder := newAdvanceGateTest(t, domain.ChapterAdvanceReview)
	if err := st.RunMeta.GrantAdvancePermit(1); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.MarkChapterComplete(1, 1000, "", ""); err != nil {
		t.Fatal(err)
	}
	if !gate.HandleBoundary() || recorder.paused != 1 {
		t.Fatal("Đã hoàn thành nhưng thiếu commit checkpoint phải báo lỗi rõ ràng và tạm dừng")
	}
	meta, _ := st.RunMeta.Load()
	if meta.AdvancePermitChapter != 1 {
		t.Fatal("Trạng thái hỏng không được đoán mò tiêu thụ giấy phép")
	}
}

func TestChapterAdvanceGateHoldLifecycle(t *testing.T) {
	st, gate, recorder := newAdvanceGateTest(t, domain.ChapterAdvanceAuto)
	hold := domain.AdvanceHold{After: domain.AdvanceHoldAfterRewritesDrained, Reason: "改完让我验收"}
	if err := st.Progress.MarkChapterComplete(1, 1000, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.SetPendingRewrites([]int{1}, "返工"); err != nil {
		t.Fatal(err)
	}
	if err := st.RunMeta.SetAdvanceHold(hold); err != nil {
		t.Fatal(err)
	}
	if gate.HandleBoundary() {
		t.Fatal("Khi viết lại chưa xả hết không được tạm dừng sớm")
	}
	if err := st.Progress.CompleteRewrite(1); err != nil {
		t.Fatal(err)
	}
	if !gate.HandleBoundary() || recorder.paused != 1 {
		t.Fatal("Sau khi viết lại xả hết phải tiêu thụ hold và tạm dừng")
	}
	meta, _ := st.RunMeta.Load()
	if meta.AdvanceHold != nil {
		t.Fatalf("Trước khi tạm dừng hold phải được tiêu thụ nguyên tử: %+v", meta.AdvanceHold)
	}
}

func TestChapterAdvanceGateStopsAfterTargetChapterCommit(t *testing.T) {
	st, gate, recorder := newAdvanceGateTest(t, domain.ChapterAdvanceAuto)
	hold := domain.AdvanceHold{After: domain.AdvanceHoldAtChapter, TargetChapter: 2, Reason: "写到第2章"}
	if err := st.RunMeta.SetAdvanceHold(hold); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.MarkChapterComplete(1, 1000, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Checkpoints.Append(domain.ChapterScope(1), "commit", "", ""); err != nil {
		t.Fatal(err)
	}
	if gate.HandleBoundary() {
		t.Fatal("Khi chương mục tiêu chưa hoàn thành không được tạm dừng")
	}
	if err := st.Progress.MarkChapterComplete(2, 1000, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Checkpoints.Append(domain.ChapterScope(2), "commit", "", ""); err != nil {
		t.Fatal(err)
	}
	if !gate.HandleBoundary() || recorder.paused != 1 {
		t.Fatal("Sau khi chương mục tiêu nộp ổn định phải tạm dừng")
	}
	if len(recorder.reasons) == 0 || !strings.Contains(recorder.reasons[len(recorder.reasons)-1], "chương 2") {
		t.Fatalf("Sự kiện tạm dừng thiếu chương mục tiêu: %v", recorder.reasons)
	}
	meta, _ := st.RunMeta.Load()
	if meta.AdvanceHold != nil {
		t.Fatalf("Trước khi tạm dừng theo chương mục tiêu phải tiêu thụ hold: %+v", meta.AdvanceHold)
	}
}

func TestChapterAdvanceGateTargetHoldWaitsForCommitRecovery(t *testing.T) {
	st, gate, recorder := newAdvanceGateTest(t, domain.ChapterAdvanceAuto)
	hold := domain.AdvanceHold{After: domain.AdvanceHoldAtChapter, TargetChapter: 1, Reason: "写到第1章"}
	if err := st.RunMeta.SetAdvanceHold(hold); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.MarkChapterComplete(1, 1000, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.Progress.MarkComplete(); err != nil {
		t.Fatal(err)
	}
	if err := st.Signals.SavePendingCommit(domain.PendingCommit{Chapter: 1, Stage: domain.CommitStageProgressMarked}); err != nil {
		t.Fatal(err)
	}
	if gate.HandleBoundary() || recorder.paused != 0 {
		t.Fatal("Khi khôi phục nộp chưa xong không được tiêu thụ hold chương mục tiêu")
	}
	meta, _ := st.RunMeta.Load()
	if meta.AdvanceHold == nil {
		t.Fatal("Trong lúc khôi phục nộp phải giữ hold chương mục tiêu")
	}
	if err := st.Signals.ClearPendingCommit(); err != nil {
		t.Fatal(err)
	}
	if !gate.HandleBoundary() || recorder.paused != 1 {
		t.Fatal("Khi bản ghi khôi phục nộp biến mất mà thiếu checkpoint phải tạm dừng rõ ràng")
	}
	meta, _ = st.RunMeta.Load()
	if meta.AdvanceHold == nil {
		t.Fatal("Trạng thái hỏng không được tiêu thụ hold chương mục tiêu")
	}
}

func TestChapterAdvanceGateTargetHoldTemporarilyAuthorizesReviewMode(t *testing.T) {
	st, gate, recorder := newAdvanceGateTest(t, domain.ChapterAdvanceReview)
	hold := domain.AdvanceHold{After: domain.AdvanceHoldAtChapter, TargetChapter: 2, Reason: "写到第2章"}
	if err := st.RunMeta.SetAdvanceHold(hold); err != nil {
		t.Fatal(err)
	}
	for chapter := 1; chapter <= 2; chapter++ {
		allowed, err := gate.Allow(&flow.Instruction{Agent: "writer", Chapter: chapter})
		if err != nil || !allowed {
			t.Fatalf("Hold chương mục tiêu phải tạm thời cho qua chương %d: allowed=%v err=%v", chapter, allowed, err)
		}
		if err := st.Progress.MarkChapterComplete(chapter, 1000, "", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Checkpoints.Append(domain.ChapterScope(chapter), "commit", "", ""); err != nil {
			t.Fatal(err)
		}
		stopped := gate.HandleBoundary()
		if chapter < 2 && stopped {
			t.Fatal("Chưa tới chương mục tiêu không được tạm dừng")
		}
		if chapter == 2 && !stopped {
			t.Fatal("Tới chương mục tiêu rồi phải tạm dừng")
		}
	}
	meta, _ := st.RunMeta.Load()
	if recorder.paused != 1 || meta.AdvanceMode != domain.ChapterAdvanceReview || meta.AdvanceHold != nil {
		t.Fatalf("Sau khi tạm dừng phải khôi phục chính sách review gốc: paused=%d meta=%+v", recorder.paused, meta)
	}
}
