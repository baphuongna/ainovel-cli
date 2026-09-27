package store

import (
	"os"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// SignalStore quản lý tệp tín hiệu dùng một lần (kết quả commit/review, trạng thái chờ khôi phục).
type SignalStore struct{ io *IO }

func NewSignalStore(io *IO) *SignalStore { return &SignalStore{io: io} }

// SaveLastCommit lưu kết quả commit gần nhất vào meta/last_commit.json.
func (s *SignalStore) SaveLastCommit(result domain.CommitResult) error {
	return s.io.WriteJSON("meta/last_commit.json", result)
}

// LoadLastCommit đọc kết quả commit gần nhất.
func (s *SignalStore) LoadLastCommit() (*domain.CommitResult, error) {
	var r domain.CommitResult
	if err := s.io.ReadJSON("meta/last_commit.json", &r); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// LoadAndClearLastCommit đọc và xóa tín hiệu commit một cách nguyên tử, ngăn race TOCTOU.
func (s *SignalStore) LoadAndClearLastCommit() (*domain.CommitResult, error) {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	var r domain.CommitResult
	if err := s.io.ReadJSONUnlocked("meta/last_commit.json", &r); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	_ = s.io.RemoveFileUnlocked("meta/last_commit.json")
	return &r, nil
}

// ClearLastCommit xóa tệp tín hiệu commit.
func (s *SignalStore) ClearLastCommit() error {
	return s.io.RemoveFile("meta/last_commit.json")
}

// SavePendingCommit lưu trạng thái nộp chương chờ khôi phục.
func (s *SignalStore) SavePendingCommit(pending domain.PendingCommit) error {
	return s.io.WriteJSON("meta/pending_commit.json", pending)
}

// LoadPendingCommit đọc trạng thái nộp chương chờ khôi phục.
func (s *SignalStore) LoadPendingCommit() (*domain.PendingCommit, error) {
	var pending domain.PendingCommit
	if err := s.io.ReadJSON("meta/pending_commit.json", &pending); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &pending, nil
}

// ClearPendingCommit xóa trạng thái nộp chương chờ khôi phục.
func (s *SignalStore) ClearPendingCommit() error {
	return s.io.RemoveFile("meta/pending_commit.json")
}

// SaveLastReview lưu kết quả xem xét gần nhất vào meta/last_review.json.
func (s *SignalStore) SaveLastReview(r domain.ReviewEntry) error {
	return s.io.WriteJSON("meta/last_review.json", r)
}

// LoadLastReviewSignal đọc tệp tín hiệu xem xét.
func (s *SignalStore) LoadLastReviewSignal() (*domain.ReviewEntry, error) {
	var r domain.ReviewEntry
	if err := s.io.ReadJSON("meta/last_review.json", &r); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// ClearLastReview xóa tệp tín hiệu xem xét.
func (s *SignalStore) ClearLastReview() error {
	return s.io.RemoveFile("meta/last_review.json")
}

// LoadAndClearLastReview đọc và xóa tín hiệu xem xét một cách nguyên tử.
func (s *SignalStore) LoadAndClearLastReview() (*domain.ReviewEntry, error) {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	var r domain.ReviewEntry
	if err := s.io.ReadJSONUnlocked("meta/last_review.json", &r); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	_ = s.io.RemoveFileUnlocked("meta/last_review.json")
	return &r, nil
}

// ClearStaleSignals dọn các tệp tín hiệu sót lại (gọi khi tiến trình khởi động lại).
func (s *SignalStore) ClearStaleSignals() {
	_ = s.io.RemoveFile("meta/last_commit.json")
	_ = s.io.RemoveFile("meta/last_review.json")
}
