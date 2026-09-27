package store

import (
	"fmt"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// CharacterStore quản lý hồ sơ nhân vật và ảnh chụp trạng thái.
type CharacterStore struct {
	io      *IO
	outline *OutlineStore // phụ thuộc chỉ đọc, dùng cho việc duyệt khi lấy ảnh chụp
}

func NewCharacterStore(io *IO, outline *OutlineStore) *CharacterStore {
	return &CharacterStore{io: io, outline: outline}
}

// Save đồng thời lưu characters.json và characters.md (ghi nguyên tử).
func (s *CharacterStore) Save(chars []domain.Character) error {
	return s.io.WithWriteLock(func() error {
		if err := s.io.WriteJSONUnlocked("characters.json", chars); err != nil {
			return err
		}
		return s.io.WriteMarkdownUnlocked("characters.md", renderCharacters(chars, s.io.labels()))
	})
}

// Load đọc hồ sơ nhân vật từ characters.json.
func (s *CharacterStore) Load() ([]domain.Character, error) {
	var chars []domain.Character
	if err := s.io.ReadJSON("characters.json", &chars); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return chars, nil
}

// SaveSnapshots lưu ảnh chụp trạng thái nhân vật vào meta/snapshots/v{vol}a{arc}.json.
func (s *CharacterStore) SaveSnapshots(volume, arc int, snapshots []domain.CharacterSnapshot) error {
	return s.io.WriteJSON(fmt.Sprintf("meta/snapshots/v%02da%02d.json", volume, arc), snapshots)
}

// LoadSnapshots đọc ảnh chụp nhân vật của cặp tập-cung chỉ định.
func (s *CharacterStore) LoadSnapshots(volume, arc int) ([]domain.CharacterSnapshot, error) {
	var snapshots []domain.CharacterSnapshot
	if err := s.io.ReadJSON(fmt.Sprintf("meta/snapshots/v%02da%02d.json", volume, arc), &snapshots); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return snapshots, nil
}

// LoadLatestSnapshots tải ảnh chụp nhân vật gần đây nhất (tìm theo thứ tự tập-cung giảm dần).
func (s *CharacterStore) LoadLatestSnapshots() ([]domain.CharacterSnapshot, error) {
	// Dàn ý thiếu → (nil,nil) đã được xử lý bên trong LoadLayeredOutline; file hỏng thì
	// phải ném lỗi lên, nếu không ảnh chụp nhân vật sẽ âm thầm biến mất cùng lúc dàn ý
	// phân tầng hỏng, quá trình viết mất tính liên tục nhân vật mà không ai hay biết.
	volumes, err := s.outline.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}
	if len(volumes) == 0 {
		return nil, nil
	}
	for vi := len(volumes) - 1; vi >= 0; vi-- {
		v := volumes[vi]
		for ai := len(v.Arcs) - 1; ai >= 0; ai-- {
			snaps, err := s.LoadSnapshots(v.Index, v.Arcs[ai].Index)
			if err != nil {
				return nil, err
			}
			if len(snaps) > 0 {
				return snaps, nil
			}
		}
	}
	return nil, nil
}

func renderCharacters(chars []domain.Character, l mdLabels) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", l.charProfiles)
	for _, c := range chars {
		fmt.Fprintf(&b, "## %s%s%s%s\n\n", c.Name, l.openParen, c.Role, l.closeParen)
		fmt.Fprintf(&b, "%s\n\n", c.Description)
		if c.Arc != "" {
			fmt.Fprintf(&b, "**%s**%s%s\n\n", l.charArc, l.colon, c.Arc)
		}
		if len(c.Traits) > 0 {
			fmt.Fprintf(&b, "**%s**%s%s\n\n", l.traits, l.colon, strings.Join(c.Traits, l.listSep))
		}
	}
	return b.String()
}
