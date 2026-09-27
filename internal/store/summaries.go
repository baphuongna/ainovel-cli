package store

import (
	"fmt"
	"os"
	"sync"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// SummaryStore quản lý tóm tắt chương, cung, tập.
type SummaryStore struct {
	io         *IO
	outline    *OutlineStore // phụ thuộc chỉ đọc, dùng lấy số cung/tập
	titleMu    sync.RWMutex
	titleCache map[int]string
}

func NewSummaryStore(io *IO, outline *OutlineStore) *SummaryStore {
	return &SummaryStore{io: io, outline: outline, titleCache: make(map[int]string)}
}

// SaveSummary lưu tóm tắt chương vào summaries/{ch}.json.
func (s *SummaryStore) SaveSummary(sum domain.ChapterSummary) error {
	if err := s.io.WriteJSON(fmt.Sprintf("summaries/%02d.json", sum.Chapter), sum); err != nil {
		return err
	}
	s.titleMu.Lock()
	s.titleCache[sum.Chapter] = sum.Title
	s.titleMu.Unlock()
	return nil
}

// LoadSummary đọc tóm tắt của chương chỉ định.
func (s *SummaryStore) LoadSummary(chapter int) (*domain.ChapterSummary, error) {
	var sum domain.ChapterSummary
	if err := s.io.ReadJSON(fmt.Sprintf("summaries/%02d.json", chapter), &sum); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &sum, nil
}

// LoadSummaryTitle đọc tiêu đề chương và cache trong tiến trình. Tiêu đề chỉ thay đổi khi
// SaveSummary, không cần decode lặp lại cùng một tóm tắt chương.
func (s *SummaryStore) LoadSummaryTitle(chapter int) (string, error) {
	s.titleMu.RLock()
	title, ok := s.titleCache[chapter]
	s.titleMu.RUnlock()
	if ok {
		return title, nil
	}
	s.titleMu.Lock()
	defer s.titleMu.Unlock()
	if title, ok := s.titleCache[chapter]; ok {
		return title, nil
	}
	sum, err := s.LoadSummary(chapter)
	if err != nil || sum == nil {
		return "", err
	}
	s.titleCache[chapter] = sum.Title
	return sum.Title, nil
}

// LoadRecentSummaries nạp tóm tắt của count chương gần nhất trước chương current.
func (s *SummaryStore) LoadRecentSummaries(current, count int) ([]domain.ChapterSummary, error) {
	var result []domain.ChapterSummary
	start := max(current-count, 1)
	for ch := start; ch < current; ch++ {
		sum, err := s.LoadSummary(ch)
		if err != nil {
			return nil, err
		}
		if sum != nil {
			result = append(result, *sum)
		}
	}
	return result, nil
}

// SaveArcSummary lưu tóm tắt cấp cung.
func (s *SummaryStore) SaveArcSummary(sum domain.ArcSummary) error {
	return s.io.WriteJSON(fmt.Sprintf("summaries/arc-v%02da%02d.json", sum.Volume, sum.Arc), sum)
}

// HasArcSummary kiểm tra cung chỉ định đã lưu tóm tắt chưa.
func (s *SummaryStore) HasArcSummary(volume, arc int) (bool, error) {
	sum, err := s.LoadArcSummary(volume, arc)
	if err != nil {
		return false, err
	}
	return sum != nil, nil
}

// HasVolumeSummary kiểm tra tập chỉ định đã lưu tóm tắt chưa.
func (s *SummaryStore) HasVolumeSummary(volume int) (bool, error) {
	sum, err := s.LoadVolumeSummary(volume)
	if err != nil {
		return false, err
	}
	return sum != nil, nil
}

// LoadArcSummary đọc tóm tắt của cung chỉ định.
func (s *SummaryStore) LoadArcSummary(volume, arc int) (*domain.ArcSummary, error) {
	var sum domain.ArcSummary
	if err := s.io.ReadJSON(fmt.Sprintf("summaries/arc-v%02da%02d.json", volume, arc), &sum); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &sum, nil
}

// LoadArcSummaries nạp toàn bộ tóm tắt cung hiện có trong một tập.
func (s *SummaryStore) LoadArcSummaries(volume int) ([]domain.ArcSummary, error) {
	maxArc := s.arcCountForVolume(volume)
	var result []domain.ArcSummary
	for arc := 1; arc <= maxArc; arc++ {
		sum, err := s.LoadArcSummary(volume, arc)
		if err != nil {
			return nil, err
		}
		if sum != nil {
			result = append(result, *sum)
		}
	}
	return result, nil
}

// SaveVolumeSummary lưu tóm tắt cấp tập.
func (s *SummaryStore) SaveVolumeSummary(sum domain.VolumeSummary) error {
	return s.io.WriteJSON(fmt.Sprintf("summaries/vol-v%02d.json", sum.Volume), sum)
}

// LoadVolumeSummary đọc tóm tắt của tập chỉ định.
func (s *SummaryStore) LoadVolumeSummary(volume int) (*domain.VolumeSummary, error) {
	var sum domain.VolumeSummary
	if err := s.io.ReadJSON(fmt.Sprintf("summaries/vol-v%02d.json", volume), &sum); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &sum, nil
}

// LoadAllVolumeSummaries nạp toàn bộ tóm tắt tập hiện có.
func (s *SummaryStore) LoadAllVolumeSummaries() ([]domain.VolumeSummary, error) {
	maxVol := s.volumeCount()
	var result []domain.VolumeSummary
	for vol := 1; vol <= maxVol; vol++ {
		sum, err := s.LoadVolumeSummary(vol)
		if err != nil {
			return nil, err
		}
		if sum != nil {
			result = append(result, *sum)
		}
	}
	return result, nil
}

// FindCharacterAppearances tìm hàng loạt số chương xuất hiện cuối của nhiều nhân vật.
func (s *SummaryStore) FindCharacterAppearances(names []string, endChapter, recentWindow int) (map[string]int, error) {
	result := make(map[string]int, len(names))
	remaining := make(map[string]struct{}, len(names))
	for _, n := range names {
		remaining[n] = struct{}{}
	}
	for ch := endChapter - recentWindow; ch >= 1; ch-- {
		if len(remaining) == 0 {
			break
		}
		sum, err := s.LoadSummary(ch)
		if err != nil {
			return nil, err
		}
		if sum == nil {
			continue
		}
		for _, c := range sum.Characters {
			if _, need := remaining[c]; need {
				result[c] = ch
				delete(remaining, c)
			}
		}
	}
	return result, nil
}

func (s *SummaryStore) volumeCount() int {
	volumes, err := s.outline.LoadLayeredOutline()
	if err == nil && len(volumes) > 0 {
		return len(volumes)
	}
	return 20
}

func (s *SummaryStore) arcCountForVolume(volume int) int {
	volumes, err := s.outline.LoadLayeredOutline()
	if err == nil {
		for _, v := range volumes {
			if v.Index == volume {
				return len(v.Arcs)
			}
		}
	}
	return 20
}
