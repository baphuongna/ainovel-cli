package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const runtimeQueuePath = "meta/runtime/queue.jsonl"

// RuntimeStore quản lý hàng đợi runtime hợp nhất và log theo từng task.
type RuntimeStore struct {
	io *IO

	mu         sync.Mutex
	seqLoaded  bool
	nextSeqNum int64
}

func NewRuntimeStore(io *IO) *RuntimeStore {
	return &RuntimeStore{io: io}
}

// AppendQueue thêm một bản ghi hàng đợi runtime và tự động cấp số thứ tự tăng dần.
func (s *RuntimeStore) AppendQueue(item domain.RuntimeQueueItem) (domain.RuntimeQueueItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureSeqLoadedLocked(); err != nil {
		return item, err
	}
	s.nextSeqNum++
	item.Seq = s.nextSeqNum
	if item.Time.IsZero() {
		item.Time = time.Now()
	}
	if err := s.appendJSONLine(runtimeQueuePath, item); err != nil {
		return item, err
	}
	return item, nil
}

// LoadQueue đọc toàn bộ mục hàng đợi runtime đã lưu bền hiện tại.
func (s *RuntimeStore) LoadQueue() ([]domain.RuntimeQueueItem, error) {
	return loadJSONLines[domain.RuntimeQueueItem](s.io, runtimeQueuePath)
}

// LoadQueueAfter trả về các mục hàng đợi sau số thứ tự chỉ định.
func (s *RuntimeStore) LoadQueueAfter(afterSeq int64) ([]domain.RuntimeQueueItem, error) {
	items, err := s.LoadQueue()
	if err != nil || afterSeq <= 0 {
		return items, err
	}
	filtered := items[:0]
	for _, item := range items {
		if item.Seq > afterSeq {
			filtered = append(filtered, item)
		}
	}
	return append([]domain.RuntimeQueueItem(nil), filtered...), nil
}

// taskIDPattern giới hạn tên tệp log task: chữ cái/số/._- và không chứa ... Một khi taskID
// trong tương lai đến từ hàng đợi hoặc đầu ra LLM, nối đường dẫn trực tiếp sẽ ghi ra ngoài
// thư mục store (review V-2.5).
var taskIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// AppendTaskLog thêm log runtime của một task.
func (s *RuntimeStore) AppendTaskLog(taskID string, entry domain.RuntimeTaskLogEntry) error {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}
	if !taskIDPattern.MatchString(taskID) || strings.Contains(taskID, "..") {
		return fmt.Errorf("task id không hợp lệ (từ chối nối đường dẫn): %q", taskID)
	}
	if entry.Time.IsZero() {
		entry.Time = time.Now()
	}
	if entry.TaskID == "" {
		entry.TaskID = taskID
	}
	return s.appendJSONLine(taskLogPath(taskID), entry)
}

// LoadTaskLog đọc toàn bộ log runtime của một task.
func (s *RuntimeStore) LoadTaskLog(taskID string) ([]domain.RuntimeTaskLogEntry, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, nil
	}
	if !taskIDPattern.MatchString(taskID) || strings.Contains(taskID, "..") {
		return nil, fmt.Errorf("task id không hợp lệ (từ chối nối đường dẫn): %q", taskID)
	}
	return loadJSONLines[domain.RuntimeTaskLogEntry](s.io, taskLogPath(taskID))
}

func taskLogPath(taskID string) string {
	return filepath.Join("meta", "runtime", "tasks", taskID+".log")
}

// Reset xóa sạch hàng đợi runtime và log task.
func (s *RuntimeStore) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seqLoaded = false
	s.nextSeqNum = 0

	var errs []string
	if err := os.Remove(filepath.Join(s.io.dir, runtimeQueuePath)); err != nil && !os.IsNotExist(err) {
		errs = append(errs, err.Error())
	}
	if err := os.RemoveAll(filepath.Join(s.io.dir, "meta", "runtime", "tasks")); err != nil {
		errs = append(errs, err.Error())
	}
	if err := os.MkdirAll(filepath.Join(s.io.dir, "meta", "runtime", "tasks"), 0o755); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("reset runtime store: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (s *RuntimeStore) ensureSeqLoadedLocked() error {
	if s.seqLoaded {
		return nil
	}
	items, err := loadJSONLines[domain.RuntimeQueueItem](s.io, runtimeQueuePath)
	if err != nil {
		return err
	}
	if len(items) > 0 {
		s.nextSeqNum = items[len(items)-1].Seq
	}
	s.seqLoaded = true
	return nil
}

func (s *RuntimeStore) appendJSONLine(rel string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return s.io.AppendLine(rel, data)
}

func loadJSONLines[T any](io *IO, rel string) ([]T, error) {
	data, err := io.ReadFile(rel)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	// JSONL kiểu append chỉ tính dòng kết thúc bằng \n là đã commit (thống nhất với giao thức
	// append_log). Phần đuôi nửa dòng do mất điện/crash để lại bị cắt tại đây, nếu không một
	// torn write sẽ làm treo vĩnh viễn headless --resume và việc nối thêm hàng đợi (review V-2 MAJOR1).
	if len(data) > 0 && data[len(data)-1] != '\n' {
		keep := bytes.LastIndexByte(data, '\n') + 1
		if terr := os.Truncate(io.path(rel), int64(keep)); terr == nil {
			slog.Warn("đã sửa phần đuôi JSONL chưa commit", "module", "store", "file", rel,
				"discarded_bytes", len(data)-keep)
			data = data[:keep]
		} else {
			slog.Warn("phát hiện đuôi chưa commit nhưng sửa thất bại", "module", "store", "file", rel, "err", terr)
		}
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 8*1024*1024)
	var out []T
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item T
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			// Dòng hoàn chỉnh bị hỏng giữ nguyên fail-loud, nhưng kèm đường dẫn sửa khả thi (review V-2.4).
			return nil, fmt.Errorf("parse %s dòng %d: %w (tệp %s; sửa: xóa dòng đó hoặc cả tệp rồi thử lại)",
				rel, lineNo, err, io.path(rel))
		}
		out = append(out, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
