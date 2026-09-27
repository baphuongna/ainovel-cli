package tools

import (
	"fmt"
	"slices"
	"sync"

	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/stylestat"
)

// StyleStatsIndex đồng bộ các chương đã hoàn thành trong Store vào bộ thống kê tăng dần.
// Lần Snapshot đầu phục hồi toàn lượng một lần; sau đó chỉ tải chương mới thêm, viết lại do commit_chapter chủ động làm mới.
type StyleStatsIndex struct {
	store *store.Store

	mu        sync.Mutex
	tracker   *stylestat.Tracker
	completed map[int]struct{}
}

func NewStyleStatsIndex(store *store.Store) *StyleStatsIndex {
	return &StyleStatsIndex{store: store}
}

func (s *StyleStatsIndex) Snapshot(
	completedChapters []int,
	titles, stopwords []string,
) (*stylestat.Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	completed, wanted, err := normalizeCompletedChapters(completedChapters)
	if err != nil {
		return nil, err
	}

	if s.tracker == nil {
		tracker := stylestat.NewTracker()
		for _, chapter := range completed {
			text, err := s.loadChapter(chapter)
			if err != nil {
				return nil, err
			}
			tracker.Upsert(chapter, text)
		}
		s.tracker = tracker
		s.completed = wanted
		return tracker.Snapshot(titles, stopwords), nil
	}

	type chapterText struct {
		chapter int
		text    string
	}
	var additions []chapterText
	for _, chapter := range completed {
		if _, ok := s.completed[chapter]; ok {
			continue
		}
		text, err := s.loadChapter(chapter)
		if err != nil {
			return nil, err
		}
		additions = append(additions, chapterText{chapter: chapter, text: text})
	}

	for chapter := range s.completed {
		if _, ok := wanted[chapter]; ok {
			continue
		}
		s.tracker.Remove(chapter)
	}
	for _, addition := range additions {
		s.tracker.Upsert(addition.chapter, addition.text)
	}
	s.completed = wanted
	return s.tracker.Snapshot(titles, stopwords), nil
}

// ChapterCommitted làm mới một chương sau khi Saga nộp hoàn tất thành công. Khi chỉ mục chưa khởi tạo,
// lần Snapshot tiếp theo sẽ phục hồi một lần từ sự kiện Progress.
func (s *StyleStatsIndex) ChapterCommitted(chapter int, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tracker == nil {
		return
	}
	s.tracker.Upsert(chapter, text)
	s.completed[chapter] = struct{}{}
}

func (s *StyleStatsIndex) loadChapter(chapter int) (string, error) {
	text, err := s.store.Drafts.LoadChapterText(chapter)
	if err != nil {
		return "", fmt.Errorf("đọc chính văn bản cuối chương %d: %w", chapter, err)
	}
	if text == "" {
		return "", fmt.Errorf("Chương %d đã đánh dấu hoàn thành nhưng chính văn bản cuối không tồn tại", chapter)
	}
	return text, nil
}

func normalizeCompletedChapters(chapters []int) ([]int, map[int]struct{}, error) {
	normalized := slices.Clone(chapters)
	slices.Sort(normalized)
	set := make(map[int]struct{}, len(normalized))
	for _, chapter := range normalized {
		if chapter <= 0 {
			return nil, nil, fmt.Errorf("Số chương đã hoàn thành phải lớn hơn 0, thực tế là %d", chapter)
		}
		if _, exists := set[chapter]; exists {
			return nil, nil, fmt.Errorf("Chương đã hoàn thành bị lặp: chương %d", chapter)
		}
		set[chapter] = struct{}{}
	}
	return normalized, set, nil
}
