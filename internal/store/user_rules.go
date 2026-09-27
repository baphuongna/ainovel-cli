package store

import (
	"os"

	"github.com/voocel/ainovel-cli/internal/rules"
)

// UserRulesStore quản lý ảnh chụp nhanh quy tắc người dùng đã chuẩn hóa của sách này
// (meta/user_rules.json).
//
// Nguồn sự thực duy nhất lúc vận hành: novel_context bơm và commit_chapter kiểm tra đều chỉ đọc một
// bản này, không đọc lặp lại tệp rules (tránh trôi dạt và hai bên đọc phân tán). Ảnh chụp sinh bằng chuẩn hóa khi mở sách/import/làm mới.
type UserRulesStore struct{ io *IO }

func NewUserRulesStore(io *IO) *UserRulesStore { return &UserRulesStore{io: io} }

// Load đọc meta/user_rules.json. Không tồn tại thì trả về nil (bên gọi căn cứ đó sinh lười).
func (s *UserRulesStore) Load() (*rules.Snapshot, error) {
	s.io.mu.RLock()
	defer s.io.mu.RUnlock()
	var snap rules.Snapshot
	if err := s.io.ReadJSONUnlocked("meta/user_rules.json", &snap); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &snap, nil
}

// Save lưu ảnh chụp nhanh.
func (s *UserRulesStore) Save(snap *rules.Snapshot) error {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	return s.io.WriteJSONUnlocked("meta/user_rules.json", snap)
}
