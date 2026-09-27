package store

import (
	"fmt"
	"os"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// RunMetaStore quản lý thông tin meta vận hành (model, lịch sử can thiệp, cấp lập kế hoạch...).
type RunMetaStore struct{ io *IO }

func NewRunMetaStore(io *IO) *RunMetaStore { return &RunMetaStore{io: io} }

// Save lưu thông tin meta vận hành vào meta/run.json.
func (s *RunMetaStore) Save(meta domain.RunMeta) error {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	return s.saveUnlocked(meta)
}

// Load đọc thông tin meta vận hành.
func (s *RunMetaStore) Load() (*domain.RunMeta, error) {
	s.io.mu.RLock()
	defer s.io.mu.RUnlock()
	return s.loadUnlocked()
}

func (s *RunMetaStore) loadUnlocked() (*domain.RunMeta, error) {
	var meta domain.RunMeta
	if err := s.io.ReadJSONUnlocked("meta/run.json", &meta); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &meta, nil
}

func (s *RunMetaStore) saveUnlocked(meta domain.RunMeta) error {
	return s.io.WriteJSONUnlocked("meta/run.json", meta)
}

// Init khởi tạo hoặc cập nhật thông tin meta vận hành; giữ lại toàn bộ sự thực ý định vận hành
// xuyên suốt khởi động lại — PlanStart đặc biệt quan trọng: sau khi crash trong giai đoạn lập kế hoạch
// (phán định khởi động đã lưu, foundation đầu tiên chưa lưu), nó là căn cứ duy nhất khôi phục danh tính
// planner; bị Init ghi đè thì việc khôi phục dừng ngay lập tức.
func (s *RunMetaStore) Init(style, provider, model string) error {
	return s.io.WithWriteLock(func() error {
		existing, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		meta := domain.RunMeta{
			StartedAt: time.Now().Format(time.RFC3339),
			Provider:  provider,
			Style:     style,
			Model:     model,
		}
		if existing != nil {
			meta.PendingSteer = existing.PendingSteer
			meta.PlanningTier = existing.PlanningTier
			meta.PlanStart = existing.PlanStart
			meta.StartPrompt = existing.StartPrompt
			meta.AdvanceMode = existing.AdvanceMode
			meta.AdvancePermitChapter = existing.AdvancePermitChapter
			meta.AdvanceHold = existing.AdvanceHold
		}
		if meta.AdvanceMode == "" {
			meta.AdvanceMode = domain.ChapterAdvanceAuto
		}
		if err := validateAdvanceControl(meta); err != nil {
			return err
		}
		return s.saveUnlocked(meta)
	})
}

func validateAdvanceControl(meta domain.RunMeta) error {
	if !meta.AdvanceMode.Valid() {
		return &domain.UnsupportedAdvanceModeError{Mode: meta.AdvanceMode}
	}
	if meta.AdvancePermitChapter < 0 {
		return fmt.Errorf("permit chương không được âm: %d", meta.AdvancePermitChapter)
	}
	if meta.AdvanceMode == domain.ChapterAdvanceAuto && meta.AdvancePermitChapter != 0 {
		return fmt.Errorf("chế độ auto không được giữ permit chương: %d", meta.AdvancePermitChapter)
	}
	if meta.AdvanceHold != nil {
		if err := meta.AdvanceHold.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// SetStartPrompt cố hóa nhu cầu sáng tác gốc của người dùng — sự thực đầu vào, lưu đĩa **trước khi**
// phán định khởi động. Khi phán định thất bại (ví dụ model lỗi) nó vẫn còn đó, khôi phục/tiếp tục để
// engine căn cứ vào đó mà phán định bù (engine.planStartFallback), khởi động thất bại không còn là
// thế cờ chết.
func (s *RunMetaStore) SetStartPrompt(prompt string) error {
	return s.io.WithWriteLock(func() error {
		meta, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if meta == nil {
			meta = &domain.RunMeta{}
		}
		meta.StartPrompt = prompt
		return s.saveUnlocked(*meta)
	})
}

// SetPendingSteer ghi lại lệnh Steer chưa hoàn tất.
func (s *RunMetaStore) SetPendingSteer(input string) error {
	return s.io.WithWriteLock(func() error {
		meta, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if meta == nil {
			meta = &domain.RunMeta{}
		}
		meta.PendingSteer = input
		return s.saveUnlocked(*meta)
	})
}

// ClearPendingSteer xóa lệnh Steer đã xử lý.
func (s *RunMetaStore) ClearPendingSteer() error {
	return s.io.WithWriteLock(func() error {
		meta, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if meta == nil || meta.PendingSteer == "" {
			return nil
		}
		meta.PendingSteer = ""
		return s.saveUnlocked(*meta)
	})
}

// SetAdvanceMode chuyển chế độ tiến chương. Khi chuyển về auto thì xóa permit chương trong cùng một khóa ghi.
func (s *RunMetaStore) SetAdvanceMode(mode domain.ChapterAdvanceMode) error {
	if !mode.Valid() {
		return &domain.UnsupportedAdvanceModeError{Mode: mode}
	}
	return s.io.WithWriteLock(func() error {
		meta, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if meta == nil {
			return fmt.Errorf("run meta chưa khởi tạo")
		}
		meta.AdvanceMode = mode
		if mode == domain.ChapterAdvanceAuto {
			meta.AdvancePermitChapter = 0
		}
		return s.saveUnlocked(*meta)
	})
}

// GrantAdvancePermit lưu bền một permit chương chính xác cho chế độ review.
func (s *RunMetaStore) GrantAdvancePermit(chapter int) error {
	if chapter <= 0 {
		return fmt.Errorf("permit chương phải lớn hơn 0: %d", chapter)
	}
	return s.io.WithWriteLock(func() error {
		meta, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if meta == nil {
			return fmt.Errorf("run meta chưa khởi tạo")
		}
		if meta.AdvanceMode != domain.ChapterAdvanceReview {
			return fmt.Errorf("chỉ chế độ nghiệm thu từng chương mới được cấp chương tiếp theo (hiện tại %s)", meta.AdvanceMode)
		}
		if meta.AdvancePermitChapter == chapter {
			return nil
		}
		if meta.AdvancePermitChapter != 0 {
			return fmt.Errorf("đã có permit chương %d, từ chối ghi đè thành chương %d", meta.AdvancePermitChapter, chapter)
		}
		meta.AdvancePermitChapter = chapter
		return s.saveUnlocked(*meta)
	})
}

// ClearAdvancePermit chỉ tiêu thụ permit chương khớp; khi đích không còn thì idempotent.
func (s *RunMetaStore) ClearAdvancePermit(chapter int) error {
	return s.io.WithWriteLock(func() error {
		meta, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if meta == nil || meta.AdvancePermitChapter == 0 {
			return nil
		}
		if meta.AdvancePermitChapter != chapter {
			return fmt.Errorf("permit chương đã thay đổi: mong đợi chương %d, thực tế chương %d", chapter, meta.AdvancePermitChapter)
		}
		meta.AdvancePermitChapter = 0
		return s.saveUnlocked(*meta)
	})
}

// SetAdvanceHold đăng ký ý định tạm dừng một lần; ý định đang hiệu lực không được phép bị
// một ý định khác ghi đè âm thầm.
func (s *RunMetaStore) SetAdvanceHold(hold domain.AdvanceHold) error {
	if err := hold.Validate(); err != nil {
		return err
	}
	return s.io.WithWriteLock(func() error {
		meta, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if meta == nil {
			return fmt.Errorf("run meta chưa khởi tạo")
		}
		if meta.AdvanceHold != nil {
			if *meta.AdvanceHold == hold {
				return nil
			}
			return fmt.Errorf("đã có ý định tạm dừng một lần (%s: %s), từ chối ghi đè", meta.AdvanceHold.After, meta.AdvanceHold.Reason)
		}
		meta.AdvanceHold = &hold
		return s.saveUnlocked(*meta)
	})
}

// ClearAdvanceHold chỉ tiêu thụ đúng ý định bên gọi vừa đọc; khi đích không còn thì idempotent.
func (s *RunMetaStore) ClearAdvanceHold(expected domain.AdvanceHold) error {
	return s.io.WithWriteLock(func() error {
		meta, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if meta == nil || meta.AdvanceHold == nil {
			return nil
		}
		if *meta.AdvanceHold != expected {
			return fmt.Errorf("ý định tạm dừng một lần đã thay đổi, từ chối xóa nhầm")
		}
		meta.AdvanceHold = nil
		return s.saveUnlocked(*meta)
	})
}

// SetPlanningTier ghi lại cấp lập kế hoạch của tác phẩm hiện tại.
func (s *RunMetaStore) SetPlanningTier(tier domain.PlanningTier) error {
	return s.io.WithWriteLock(func() error {
		meta, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if meta == nil {
			meta = &domain.RunMeta{}
		}
		meta.PlanningTier = tier
		return s.saveUnlocked(*meta)
	})
}

// SetStyle cập nhật style của sách đang mở (ví dụ style vừa suy từ thể loại trong user_rules)
// mà không đụng các trường vận hành khác — khác Init (ghi lại toàn bộ ngữ cảnh khởi động).
// Style này là điểm neo sticky theo từng sách: lần mở sau host nạp bundle theo nó.
func (s *RunMetaStore) SetStyle(style string) error {
	return s.io.WithWriteLock(func() error {
		meta, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if meta == nil {
			meta = &domain.RunMeta{}
		}
		meta.Style = style
		return s.saveUnlocked(*meta)
	})
}

// SetPlanStart cố hóa sự thực phán định khởi động (phán định lưu sự thực trước rồi mới khởi chạy;
// crash trong giai đoạn lập kế hoạch căn cứ vào đó mà chạy tiếp khi khôi phục).
func (s *RunMetaStore) SetPlanStart(rec domain.PlanStartRecord) error {
	return s.io.WithWriteLock(func() error {
		meta, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if meta == nil {
			meta = &domain.RunMeta{}
		}
		meta.PlanStart = &rec
		return s.saveUnlocked(*meta)
	})
}
