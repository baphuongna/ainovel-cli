package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// IO đóng gói các thao tác đọc ghi hệ thống file, cung cấp khoá và ghi nguyên tử.
// Mỗi kho con giữ một instance IO riêng, với sync.RWMutex riêng của mình.
type IO struct {
	dir  string
	lang string
	mu   sync.RWMutex
}

// SetLanguage đặt ngôn ngữ tác phẩm ("vi" / "zh"), ảnh hưởng tới nhãn của các view
// Markdown dẫn xuất. Đặt một lần lúc khởi động; giá trị rỗng thì theo mặc định tiếng
// Trung của thượng nguồn.
func (io *IO) SetLanguage(lang string) { io.lang = lang }

func (io *IO) labels() mdLabels { return labelsFor(io.lang) }

func newIO(dir string) *IO {
	return &IO{dir: dir}
}

func (io *IO) path(rel string) string {
	return filepath.Join(io.dir, rel)
}

func (io *IO) ReadFile(rel string) ([]byte, error) {
	io.mu.RLock()
	defer io.mu.RUnlock()
	return io.ReadFileUnlocked(rel)
}

func (io *IO) ReadFileUnlocked(rel string) ([]byte, error) {
	return os.ReadFile(io.path(rel))
}

func (io *IO) WriteFileUnlocked(rel string, data []byte) error {
	p := io.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), filepath.Base(p)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, p)
}

func (io *IO) ReadJSON(rel string, v any) error {
	io.mu.RLock()
	defer io.mu.RUnlock()
	return io.ReadJSONUnlocked(rel, v)
}

func (io *IO) ReadJSONUnlocked(rel string, v any) error {
	data, err := io.ReadFileUnlocked(rel)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func (io *IO) WriteJSON(rel string, v any) error {
	io.mu.Lock()
	defer io.mu.Unlock()
	return io.WriteJSONUnlocked(rel, v)
}

func (io *IO) WriteJSONUnlocked(rel string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return io.WriteFileUnlocked(rel, data)
}

func (io *IO) WriteMarkdown(rel string, content string) error {
	io.mu.Lock()
	defer io.mu.Unlock()
	return io.WriteFileUnlocked(rel, []byte(content))
}

// WriteMarkdownUnlocked ghi ra .md sidecar. Quy ước: mỗi .md đều là view dễ đọc cho
// con người theo kiểu best-effort của .json tương ứng, tuyệt đối không phải nguồn dữ
// liệu — runtime và export luôn render lại từ .json. Các phương thức Save trong cùng
// một khoá ghi viết .json trước rồi mới viết .md này, là hai lần tmp+rename độc lập;
// crash giữa hai bước sẽ để lại .md tụt hậu so với .json, điều này chấp nhận được
// (không ai đọc .md như dữ liệu, lần viết cùng scope kế tiếp là tự chữa lành). Cố ý
// không thêm commit nguyên tử hai file cho việc này — đó là thiết kế thừa.
func (io *IO) WriteMarkdownUnlocked(rel string, content string) error {
	return io.WriteFileUnlocked(rel, []byte(content))
}

func (io *IO) AppendLine(rel string, data []byte) error {
	io.mu.Lock()
	defer io.mu.Unlock()
	return io.AppendLineUnlocked(rel, data)
}

func (io *IO) AppendLineUnlocked(rel string, data []byte) error {
	p := io.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// 0o600: sessions/*.jsonl là bản ghi chép đầy đủ prompt/response, thuộc dữ liệu
	// riêng tư (review F6).
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

// syncFileUnlocked xác nhận bản ghi thêm vào đã tồn tại được bền vững hoá khi replay
// idempotent. Bên gọi chịu trách nhiệm giữ khoá ghi io.mu.
func (io *IO) syncFileUnlocked(rel string) error {
	f, err := os.OpenFile(io.path(rel), os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (io *IO) RemoveFile(rel string) error {
	io.mu.Lock()
	defer io.mu.Unlock()
	return io.RemoveFileUnlocked(rel)
}

func (io *IO) RemoveFileUnlocked(rel string) error {
	err := os.Remove(io.path(rel))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (io *IO) WithWriteLock(fn func() error) error {
	io.mu.Lock()
	defer io.mu.Unlock()
	return fn()
}

// EnsureDirs tạo các thư mục con được chỉ định.
func (io *IO) EnsureDirs(dirs []string) error {
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(io.dir, d), 0o755); err != nil {
			return fmt.Errorf("create dir %s: %w", d, err)
		}
	}
	return nil
}
