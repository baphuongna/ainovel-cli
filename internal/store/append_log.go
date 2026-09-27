package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
)

// appendLog quản lý lưu trữ JSONL cho các dữ kiện chỉ tăng thêm (append-only).
// Bên gọi chịu trách nhiệm giữ khoá ghi io.mu.
//
// Lần tải đầu tiên dựng chỉ mục khử trùng trong bộ nhớ; khi thêm bản ghi (append)
// bình thường chỉ ghi các bản ghi mới. Mảng JSON phiên bản cũ được migrate một lần
// vào lần thêm đầu tiên, sau khi JSONL ghi xuống đĩa thành công mới xoá file cũ,
// nên mọi cửa sổ gián đoạn đều có thể replay lại.
type appendLog[T any] struct {
	path       string
	legacyPath string
	key        func(T) string
	clone      func(T) T

	loaded        bool
	hasLog        bool
	legacyPresent bool
	values        []T
	seen          map[string]struct{}
}

func newAppendLog[T any](path, legacyPath string, key func(T) string, clone func(T) T) *appendLog[T] {
	return &appendLog[T]{path: path, legacyPath: legacyPath, key: key, clone: clone}
}

func (l *appendLog[T]) loadUnlocked(io *IO) error {
	if l.loaded {
		return nil
	}
	l.reset()

	data, err := committedJSONLinesUnlocked(io, l.path)
	switch {
	case err == nil:
		values, err := decodeJSONLines[T](l.path, data)
		if err != nil {
			return err
		}
		l.hasLog = true
		l.setValues(values)
	case os.IsNotExist(err):
		var legacy []T
		if err := io.ReadJSONUnlocked(l.legacyPath, &legacy); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		} else {
			l.legacyPresent = true
		}
		l.setValues(legacy)
	default:
		return err
	}

	if !l.legacyPresent {
		if _, err := os.Stat(io.path(l.legacyPath)); err == nil {
			l.legacyPresent = true
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	l.loaded = true
	return nil
}

func (l *appendLog[T]) allUnlocked(io *IO) ([]T, error) {
	if err := l.loadUnlocked(io); err != nil {
		return nil, err
	}
	return l.cloneValues(l.values), nil
}

// appendUnlocked trả về các bản ghi thực sự được thêm mới. Ngay cả khi dọn dẹp file
// cũ thất bại, các bản ghi mới đã commit vào JSONL vẫn được trả về; bên gọi có thể dựa
// vào đó để đánh dấu projection dẫn xuất là cần sửa; lần replay kế tiếp chỉ việc dọn dẹp.
func (l *appendLog[T]) appendUnlocked(io *IO, incoming []T) ([]T, error) {
	if err := l.loadUnlocked(io); err != nil {
		return nil, err
	}

	added := make([]T, 0, len(incoming))
	pending := make(map[string]struct{}, len(incoming))
	for _, value := range incoming {
		key := l.key(value)
		if _, ok := l.seen[key]; ok {
			continue
		}
		if _, ok := pending[key]; ok {
			continue
		}
		pending[key] = struct{}{}
		added = append(added, l.clone(value))
	}

	if !l.hasLog && (l.legacyPresent || len(added) > 0) {
		all := append(l.cloneValues(l.values), l.cloneValues(added)...)
		data, err := encodeJSONLines(all)
		if err != nil {
			return nil, err
		}
		if err := io.WriteFileUnlocked(l.path, data); err != nil {
			l.reset()
			return nil, err
		}
		l.hasLog = true
	} else if len(added) > 0 {
		data, err := encodeJSONLines(added)
		if err != nil {
			return nil, err
		}
		if err := io.AppendLineUnlocked(l.path, data); err != nil {
			// Lần ghi có thể để lại đuôi thiếu ký tự xuống dòng. Huỷ bộ nhớ đệm, để lần tải
			// kế tiếp theo giao thức commit cắt bỏ rõ ràng đuôi chưa commit rồi mới replay.
			l.reset()
			return nil, err
		}
	} else if len(incoming) > 0 && l.hasLog {
		// Lần thêm trước có thể đã ghi xong bản ghi hoàn chỉnh nhưng Sync trả về lỗi.
		// Replay idempotent đồng bộ lại lần nữa trước khi xác nhận thành công, không nhầm
		// "đang đọc được hiện tại" thành "đã bền vững hoá".
		if err := io.syncFileUnlocked(l.path); err != nil {
			l.reset()
			return nil, err
		}
	}

	for _, value := range added {
		cloned := l.clone(value)
		l.values = append(l.values, cloned)
		l.seen[l.key(cloned)] = struct{}{}
	}

	if l.hasLog && l.legacyPresent {
		if err := io.RemoveFileUnlocked(l.legacyPath); err != nil {
			return l.cloneValues(added), err
		}
		l.legacyPresent = false
		slog.Info("Dữ kiện chỉ-tăng đã được chuyển sang nhật ký append-only",
			"module", "store", "from", l.legacyPath, "to", l.path, "records", len(l.values))
	}
	return l.cloneValues(added), nil
}

func (l *appendLog[T]) replaceUnlocked(io *IO, values []T) error {
	data, err := encodeJSONLines(values)
	if err != nil {
		return err
	}
	if err := io.WriteFileUnlocked(l.path, data); err != nil {
		l.reset()
		return err
	}
	l.hasLog = true
	l.setValues(values)
	l.loaded = true
	if err := io.RemoveFileUnlocked(l.legacyPath); err != nil {
		l.legacyPresent = true
		return err
	}
	l.legacyPresent = false
	return nil
}

func (l *appendLog[T]) setValues(values []T) {
	l.values = l.cloneValues(values)
	l.seen = make(map[string]struct{}, len(values))
	for _, value := range l.values {
		l.seen[l.key(value)] = struct{}{}
	}
}

func (l *appendLog[T]) cloneValues(values []T) []T {
	if len(values) == 0 {
		return nil
	}
	cloned := make([]T, len(values))
	for i, value := range values {
		cloned[i] = l.clone(value)
	}
	return cloned
}

func (l *appendLog[T]) reset() {
	l.loaded = false
	l.hasLog = false
	l.legacyPresent = false
	l.values = nil
	l.seen = nil
}

func encodeJSONLines[T any](values []T) ([]byte, error) {
	var data bytes.Buffer
	for i, value := range values {
		line, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode jsonl record %d: %w", i+1, err)
		}
		data.Write(line)
		data.WriteByte('\n')
	}
	return data.Bytes(), nil
}

func decodeJSONLines[T any](path string, data []byte) ([]T, error) {
	lines := bytes.Split(data, []byte{'\n'})
	values := make([]T, 0, len(lines))
	for i, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var value T
		if err := json.Unmarshal(line, &value); err != nil {
			return nil, fmt.Errorf("parse %s line %d: %w", path, i+1, err)
		}
		values = append(values, value)
	}
	return values, nil
}

// committedJSONLinesUnlocked loại bỏ phần đuôi có thể chứng minh là chưa commit
// theo giao thức: chỉ bản ghi JSONL kết thúc bằng ký tự xuống dòng mới được tính là
// đã commit. Dòng hoàn chỉnh bị hỏng vẫn báo lỗi nghiêm ngặt, không tự sửa theo kiểu đoán mò.
func committedJSONLinesUnlocked(io *IO, path string) ([]byte, error) {
	data, err := io.ReadFileUnlocked(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return data, nil
	}
	keep := bytes.LastIndexByte(data, '\n') + 1
	if err := os.Truncate(io.path(path), int64(keep)); err != nil {
		return nil, err
	}
	slog.Warn("Đã sửa đuôi chưa commit của nhật ký append-only",
		"module", "store", "file", path, "discarded_bytes", len(data)-keep)
	return data[:keep], nil
}
