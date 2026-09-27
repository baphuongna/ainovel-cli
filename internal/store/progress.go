package store

import (
	"fmt"
	"os"
	"slices"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
)

// ProgressStore quản lý trạng thái tiến độ sáng tác.
type ProgressStore struct{ io *IO }

func NewProgressStore(io *IO) *ProgressStore { return &ProgressStore{io: io} }

// Load đọc meta/progress.json. Không tồn tại thì trả về nil.
func (s *ProgressStore) Load() (*domain.Progress, error) {
	s.io.mu.RLock()
	defer s.io.mu.RUnlock()
	return s.loadUnlocked()
}

func (s *ProgressStore) loadUnlocked() (*domain.Progress, error) {
	var p domain.Progress
	if err := s.io.ReadJSONUnlocked("meta/progress.json", &p); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// Save lưu tiến độ.
func (s *ProgressStore) Save(p *domain.Progress) error {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	return s.saveUnlocked(p)
}

func (s *ProgressStore) saveUnlocked(p *domain.Progress) error {
	return s.io.WriteJSONUnlocked("meta/progress.json", p)
}

// Init tạo tiến độ ban đầu.
func (s *ProgressStore) Init(totalChapters int) error {
	return s.Save(&domain.Progress{
		Phase:         domain.PhaseInit,
		TotalChapters: totalChapters,
	})
}

// SetTotalChapters cập nhật dung lượng dàn ý: chế độ không phân tầng là số chương chi tiết, chế độ phân tầng là ước tính nội bộ.
func (s *ProgressStore) SetTotalChapters(n int) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			p = &domain.Progress{}
		}
		p.TotalChapters = n
		return s.saveUnlocked(p)
	})
}

// UpdatePhase cập nhật giai đoạn sáng tác.
func (s *ProgressStore) UpdatePhase(phase domain.Phase) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			p = &domain.Progress{}
		}
		if err := domain.ValidatePhaseTransition(p.Phase, phase); err != nil {
			return err
		}
		p.Phase = phase
		return s.saveUnlocked(p)
	})
}

// AdvancePhase đẩy giai đoạn sáng tác tiến lên ít nhất đến phase; nếu đã ở giai đoạn muộn hơn thì giữ nguyên.
// Áp dụng cho artifact giai đoạn có thể lưu lặp lại, tránh artifact cũ bị sửa bị đánh giá nhầm là lùi giai đoạn.
func (s *ProgressStore) AdvancePhase(phase domain.Phase) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			p = &domain.Progress{}
		}
		if domain.CanTransitionPhase(phase, p.Phase) {
			return nil
		}
		if err := domain.ValidatePhaseTransition(p.Phase, phase); err != nil {
			return err
		}
		p.Phase = phase
		return s.saveUnlocked(p)
	})
}

// StartChapter đánh dấu một chương vào trạng thái đang viết. Nó không được đảm nhận trách nhiệm di chuyển
// giai đoạn; bên gọi phải để quy trình foundation/import tiến Progress lên writing một cách tường minh trước,
// tránh giao việc sai vượt qua giai đoạn lập kế hoạch.
func (s *ProgressStore) StartChapter(chapter int) error {
	if chapter <= 0 {
		return fmt.Errorf("chapter must be > 0")
	}
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("progress chưa khởi tạo: %w", errs.ErrToolPrecondition)
		}
		if p.Phase != domain.PhaseWriting {
			return fmt.Errorf("viết chương chỉ được phép ở giai đoạn writing (phase hiện tại=%s): %w", p.Phase, errs.ErrToolPrecondition)
		}
		if p.Flow != domain.FlowRewriting && p.Flow != domain.FlowPolishing {
			p.Flow = domain.FlowWriting
		}
		if p.CurrentChapter < chapter {
			p.CurrentChapter = chapter
		}
		p.InProgressChapter = chapter
		p.CompletedScenes = nil
		return s.saveUnlocked(p)
	})
}

// IsChapterCompleted kiểm tra chương đã nộp hoàn thành chưa. Lỗi đọc trả về tường minh,
// không được coi progress hỏng là “chưa hoàn thành” rồi tiếp tục ghi đè chương.
func (s *ProgressStore) IsChapterCompleted(chapter int) (bool, error) {
	p, err := s.Load()
	if err != nil {
		return false, err
	}
	if p == nil {
		return false, nil
	}
	return slices.Contains(p.CompletedChapters, chapter), nil
}

// MarkChapterComplete đánh dấu chương hoàn thành, cập nhật tiến độ nguyên tử.
func (s *ProgressStore) MarkChapterComplete(chapter, wordCount int, hookType, dominantStrand string) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("progress not initialized, call Init first")
		}
		if p.ChapterWordCounts == nil {
			p.ChapterWordCounts = make(map[int]int)
		}
		if oldWC, ok := p.ChapterWordCounts[chapter]; ok {
			p.TotalWordCount -= oldWC
		}
		p.ChapterWordCounts[chapter] = wordCount
		p.TotalWordCount += wordCount
		if !slices.Contains(p.CompletedChapters, chapter) {
			p.CompletedChapters = append(p.CompletedChapters, chapter)
		}
		if chapter+1 > p.CurrentChapter {
			p.CurrentChapter = chapter + 1
		}
		p.InProgressChapter = 0
		p.CompletedScenes = nil
		if err := domain.ValidatePhaseTransition(p.Phase, domain.PhaseWriting); err != nil {
			return err
		}
		p.Phase = domain.PhaseWriting

		if dominantStrand != "" {
			for len(p.StrandHistory) < chapter-1 {
				p.StrandHistory = append(p.StrandHistory, "")
			}
			if len(p.StrandHistory) < chapter {
				p.StrandHistory = append(p.StrandHistory, dominantStrand)
			} else {
				p.StrandHistory[chapter-1] = dominantStrand
			}
		}
		if hookType != "" {
			for len(p.HookHistory) < chapter-1 {
				p.HookHistory = append(p.HookHistory, "")
			}
			if len(p.HookHistory) < chapter {
				p.HookHistory = append(p.HookHistory, hookType)
			} else {
				p.HookHistory[chapter-1] = hookType
			}
		}

		return s.saveUnlocked(p)
	})
}

// MarkComplete đánh dấu toàn bộ sáng tác hoàn thành, đồng thời xóa cờ mở lại — viết lại
// (hoàn thành tức không còn ở trạng thái viết lại).
func (s *ProgressStore) MarkComplete() error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			p = &domain.Progress{}
		}
		if err := domain.ValidatePhaseTransition(p.Phase, domain.PhaseComplete); err != nil {
			return err
		}
		p.Phase = domain.PhaseComplete
		p.ReopenedFromComplete = false
		return s.saveUnlocked(p)
	})
}

// Reopen mở lại sách đã hoàn thành vào trạng thái viết lại: phase complete→writing + chương đích vào
// hàng đợi + flow=rewriting, hoàn tất nguyên tử trong một khóa ghi. Đây là lối thoát duy nhất khỏi ràng
// buộc “chỉ tiến” của phaseOrder — cố ý không đi qua ValidatePhaseTransition; tính hợp lệ của việc lùi
// hội tụ về phương thức này và được bảo vệ bởi điều kiện trước phase=complete, tránh lạm dụng làm máy
// trạng thái mất kiểm soát. Sau khi sửa xong hàng đợi, commit_chapter sẽ tự động hoàn thành lại.
func (s *ProgressStore) Reopen(chapters []int, reason string) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("progress chưa khởi tạo: %w", errs.ErrToolPrecondition)
		}
		if p.Phase != domain.PhaseComplete {
			return fmt.Errorf("reopen chỉ áp dụng cho sách đã hoàn thành (phase hiện tại=%s): %w", p.Phase, errs.ErrToolPrecondition)
		}
		normalized, err := normalizePendingRewrites(chapters, p.CompletedChapters)
		if err != nil {
			return err
		}
		p.Phase = domain.PhaseWriting // lùi hợp pháp duy nhất, được bảo vệ bởi điều kiện complete phía trên
		p.PendingRewrites = normalized
		p.RewriteReason = reason
		p.Flow = domain.FlowRewriting
		p.ReopenedFromComplete = true // sau khi xả hết hàng đợi sẽ hoàn thành lại theo cấu trúc, xem khối drain trong commit_chapter
		return s.saveUnlocked(p)
	})
}

// ReopenContinue mở lại sách đã hoàn thành sang trạng thái viết tiếp: chỉ phase complete→writing, không
// thêm vào hàng đợi viết lại, không đặt ReopenedFromComplete (đó là ngữ nghĩa drain “hoàn thành lại tự động
// theo cấu trúc gốc sau khi xả hết hàng đợi viết lại”, còn mở lại để viết tiếp lại muốn mở rộng cấu trúc).
// Cùng với Reopen là lối thoát khỏi ràng buộc “chỉ tiến” của phaseOrder, cùng được bảo vệ bởi điều kiện
// trước phase=complete; sau khi mở lại, routing cuối tập sẽ giao kiến trúc sư viết tiếp tập mới.
func (s *ProgressStore) ReopenContinue() error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("progress chưa khởi tạo: %w", errs.ErrToolPrecondition)
		}
		if p.Phase != domain.PhaseComplete {
			return fmt.Errorf("mở lại chỉ áp dụng cho sách đã hoàn thành (phase hiện tại=%s): %w", p.Phase, errs.ErrToolPrecondition)
		}
		p.Phase = domain.PhaseWriting
		p.ReopenCount++ // kiểm toán + đảm bảo digest progress khi hoàn thành lại khác lần trước (xem chú thích trường)
		return s.saveUnlocked(p)
	})
}

// ClearInProgress xóa trạng thái trung gian của tiến độ.
func (s *ProgressStore) ClearInProgress() error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		p.InProgressChapter = 0
		p.CompletedScenes = nil
		return s.saveUnlocked(p)
	})
}

// UpdateVolumeArc cập nhật vị trí tập/cung hiện tại.
func (s *ProgressStore) UpdateVolumeArc(volume, arc int) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		p.CurrentVolume = volume
		p.CurrentArc = arc
		return s.saveUnlocked(p)
	})
}

// SetLayered đặt cờ chế độ phân tầng.
func (s *ProgressStore) SetLayered(layered bool) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		p.Layered = layered
		return s.saveUnlocked(p)
	})
}

// SetFlow cập nhật trạng thái flow hiện tại.
func (s *ProgressStore) SetFlow(flow domain.FlowState) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		if err := domain.ValidateFlowTransition(p.Flow, flow); err != nil {
			return err
		}
		p.Flow = flow
		return s.saveUnlocked(p)
	})
}

// SetPendingRewrites đặt hàng đợi chương cần viết lại và lý do.
// PendingRewrites chỉ được chứa chương đã hoàn thành; chương chưa hoàn thành chưa có chính văn
// bản cuối, không thể vào hàng đợi viết lại/đánh bóng.
func (s *ProgressStore) SetPendingRewrites(chapters []int, reason string) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		normalized, err := normalizePendingRewrites(chapters, p.CompletedChapters)
		if err != nil {
			return err
		}
		p.PendingRewrites = normalized
		p.RewriteReason = reason
		return s.saveUnlocked(p)
	})
}

// ApplyReviewOutcome áp dụng nguyên tử trạng thái flow do bước xem xét tạo ra. Ngữ nghĩa xem xét do
// tầng trên quyết định; Store chỉ chịu trách nhiệm kiểm tra chuyển đổi Flow và chương viết lại, bảo đảm
// Flow, PendingRewrites, RewriteReason không xuất hiện trạng thái lửng.
func (s *ProgressStore) ApplyReviewOutcome(flow domain.FlowState, chapters []int, reason string) (*domain.Progress, error) {
	var latest *domain.Progress
	err := s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("progress chưa khởi tạo: %w", errs.ErrToolPrecondition)
		}
		if len(chapters) > 0 {
			if flow == domain.FlowWriting {
				return fmt.Errorf("khi có chương viết lại thì flow không được là writing: %w", errs.ErrToolConflict)
			}
			if err := domain.ValidateFlowTransition(p.Flow, flow); err != nil {
				return err
			}
			normalized, err := normalizePendingRewrites(chapters, p.CompletedChapters)
			if err != nil {
				return err
			}
			p.PendingRewrites = normalized
			p.RewriteReason = reason
			p.Flow = flow
		} else if len(p.PendingRewrites) == 0 {
			if err := domain.ValidateFlowTransition(p.Flow, flow); err != nil {
				return err
			}
			p.Flow = flow
		}
		if err := s.saveUnlocked(p); err != nil {
			return err
		}
		latest = p
		return nil
	})
	return latest, err
}

// ValidatePendingRewrites kiểm tra danh sách chương có thể vào hàng đợi viết lại hay không, không sửa trạng thái.
func (s *ProgressStore) ValidatePendingRewrites(chapters []int) error {
	s.io.mu.RLock()
	defer s.io.mu.RUnlock()

	p, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	if p == nil {
		_, err := normalizePendingRewrites(chapters, nil)
		return err
	}
	_, err = normalizePendingRewrites(chapters, p.CompletedChapters)
	return err
}

// CompleteRewrite loại chương đã hoàn thành khỏi hàng đợi viết lại.
func (s *ProgressStore) CompleteRewrite(chapter int) error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		var remaining []int
		for _, ch := range p.PendingRewrites {
			if ch != chapter {
				remaining = append(remaining, ch)
			}
		}
		p.PendingRewrites = remaining
		if len(remaining) == 0 {
			if err := domain.ValidateFlowTransition(p.Flow, domain.FlowWriting); err != nil {
				return err
			}
			p.Flow = domain.FlowWriting
			p.RewriteReason = ""
		}
		return s.saveUnlocked(p)
	})
}

// ClearPendingRewrites cưỡng chế xóa sạch hàng đợi viết lại.
func (s *ProgressStore) ClearPendingRewrites() error {
	return s.io.WithWriteLock(func() error {
		p, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		p.PendingRewrites = nil
		p.RewriteReason = ""
		if err := domain.ValidateFlowTransition(p.Flow, domain.FlowWriting); err != nil {
			return err
		}
		p.Flow = domain.FlowWriting
		return s.saveUnlocked(p)
	})
}

// ValidateChapterWork kiểm tra chương hiện tại có được phép lập kế hoạch hay nộp không.
// Writer chỉ làm việc ở giai đoạn writing; dưới flow đánh bóng/viết lại, chỉ được xử lý chương
// nằm trong PendingRewrites. Ràng buộc giai đoạn được canh thêm một lần tại biên Store,
// tránh Arbiter giao việc sai vượt qua Router.
func (s *ProgressStore) ValidateChapterWork(chapter int) error {
	p, err := s.Load()
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("progress chưa khởi tạo: %w", errs.ErrToolPrecondition)
	}
	if p.Phase != domain.PhaseWriting {
		return fmt.Errorf("viết chương chỉ được phép ở giai đoạn writing (phase hiện tại=%s): %w", p.Phase, errs.ErrToolPrecondition)
	}
	if p.Flow != domain.FlowRewriting && p.Flow != domain.FlowPolishing {
		return nil
	}
	if _, err := normalizePendingRewrites(p.PendingRewrites, p.CompletedChapters); err != nil {
		return err
	}
	if slices.Contains(p.PendingRewrites, chapter) {
		return nil
	}

	verb := "viết lại"
	if p.Flow == domain.FlowPolishing {
		verb = "đánh bóng"
	}
	return fmt.Errorf("Chương %d không nằm trong hàng đợi %s, hàng đợi hiện tại: %v. Hãy xử lý xong chương trong hàng đợi trước khi động vào chương mới: %w", chapter, verb, p.PendingRewrites, errs.ErrToolConflict)
}

func normalizePendingRewrites(chapters, completed []int) ([]int, error) {
	if len(chapters) == 0 {
		return nil, nil
	}
	completedSet := make(map[int]struct{}, len(completed))
	for _, ch := range completed {
		completedSet[ch] = struct{}{}
	}

	seen := make(map[int]struct{}, len(chapters))
	normalized := make([]int, 0, len(chapters))
	var invalid []int
	for _, ch := range chapters {
		if ch <= 0 {
			invalid = append(invalid, ch)
			continue
		}
		if _, ok := completedSet[ch]; !ok {
			invalid = append(invalid, ch)
			continue
		}
		if _, ok := seen[ch]; ok {
			continue
		}
		seen[ch] = struct{}{}
		normalized = append(normalized, ch)
	}
	if len(invalid) > 0 {
		return nil, fmt.Errorf("pending_rewrites chỉ được chứa chương đã hoàn thành, chương không hợp lệ: %v, completed_chapters=%v: %w", invalid, completed, errs.ErrToolPrecondition)
	}
	return normalized, nil
}
