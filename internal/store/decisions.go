package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// DecisionStore kiểm toán các phán định ngữ nghĩa LLM lúc chạy (meta/decisions.jsonl,
// append-only).
//
// Định vị (docs/engine-arbiter.md §4.3): nguồn dữ liệu cho kiểm toán và replay offline —
// ghi lại "lúc đó thấy dữ kiện gì, đưa ra phán định gì", phục vụ hồi quy eval và đối
// chiếu A/B cho Arbiter tương lai. Nó **không phải** event sourcing, cũng **không phải**
// nguồn khôi phục (khôi phục chỉ dựa vào lớp dữ kiện như Progress/Checkpoint/RunMeta).
type DecisionStore struct{ io *IO }

func NewDecisionStore(io *IO) *DecisionStore { return &DecisionStore{io: io} }

const (
	decisionSchemaVersion = 1
	decisionsFile         = "meta/decisions.jsonl"
	// maxDecisionInputBytes giới hạn trên của một input; vượt giới hạn thì cắt cụt
	// và đánh dấu, tránh bản dán dài làm nổ file kiểm toán.
	maxDecisionInputBytes = 8 << 10
)

// DecisionRecord bản ghi kiểm toán cho một phán định ngữ nghĩa. facts chỉ lưu dữ kiện
// có cấu trúc và tham chiếu, không sao chép nội dung chính.
// input được giữ trong bản ghi (bắt buộc cho replay offline); việc che dữ liệu nhạy cảm
// xảy ra ở biên diag export, không phải lúc ghi xuống đĩa.
type DecisionRecord struct {
	SchemaVersion  int             `json:"schema_version"`
	ID             string          `json:"id"`
	At             string          `json:"at"`
	Kind           string          `json:"kind"`    // intervention | plan_start | volume_end | ...
	Decider        string          `json:"decider"` // arbiter | architect (xem xét cuối tập)
	CheckpointSeq  int64           `json:"checkpoint_seq,omitempty"`
	Input          string          `json:"input,omitempty"`
	InputTruncated bool            `json:"input_truncated,omitempty"`
	Facts          json.RawMessage `json:"facts,omitempty"`
	Decision       json.RawMessage `json:"decision,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	Error          string          `json:"error,omitempty"` // văn bản lỗi khi phán định thất bại — thất bại cũng là dữ kiện kiểm toán, thiếu nó việc xử lý sự cố chỉ còn cách suy đoán
	Model          string          `json:"model,omitempty"`
	DurationMs     int64           `json:"duration_ms,omitempty"`
}

// Append ghi xuống đĩa một bản ghi phán định; SchemaVersion/At/ID do phương thức này
// điền cho đủ, input vượt giới hạn thì cắt cụt.
// Trả về bản ghi đã điền đủ (ID để bên gọi liên kết, ví dụ PlanStartRecord.DecisionID).
func (s *DecisionStore) Append(rec DecisionRecord) (DecisionRecord, error) {
	rec.SchemaVersion = decisionSchemaVersion
	if rec.At == "" {
		rec.At = time.Now().Format(time.RFC3339)
	}
	if rec.ID == "" {
		rec.ID = newDecisionID()
	}
	if len(rec.Input) > maxDecisionInputBytes {
		rec.Input = rec.Input[:maxDecisionInputBytes]
		rec.InputTruncated = true
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return rec, fmt.Errorf("marshal decision: %w", err)
	}
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	// Lần thêm trước có thể đã crash trước khi ghi ký tự xuống dòng. Loại bỏ trước phần
	// đuôi có thể chứng minh là chưa commit theo giao thức, tránh nối JSON mới trực tiếp
	// vào sau dòng dở dang; bản ghi hoàn chỉnh kết thúc bằng xuống dòng tuyệt đối không
	// tự động sửa đổi.
	if _, err := s.committedDataUnlocked(); err != nil {
		return rec, fmt.Errorf("repair decision tail: %w", err)
	}
	if err := s.io.AppendLineUnlocked(decisionsFile, append(data, '\n')); err != nil {
		return rec, err
	}
	return rec, nil
}

// Recent trả về n bản ghi gần đây nhất (cũ→mới); file thiếu thì trả về rỗng.
//
// Dòng đã commit mà hỏng phải trả về lỗi tường minh — Arbiter không thể tiếp tục phán
// định trên gói dữ kiện thiếu một phần lịch sử. Dòng dở dang ở đuôi bị crash cắt ngang
// (byte cuối không phải '\n') do committedDataUnlocked cắt bỏ và cảnh báo tường minh;
// đây không phải sửa kiểu đoán mò, vì giao thức file này quy định chỉ bản ghi kết thúc
// bằng ký tự xuống dòng mới được tính là đã commit.
func (s *DecisionStore) Recent(n int) ([]DecisionRecord, error) {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	data, err := s.committedDataUnlocked()
	if err != nil {
		return nil, err
	}
	all, err := parseDecisionRecords(data)
	if err != nil {
		return nil, err
	}
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}

// committedDataUnlocked trả về các bản ghi hoàn chỉnh có xuống dòng, đồng thời cắt khỏi
// đĩa các byte sót lại sau ký tự xuống dòng. Bên gọi phải giữ khoá ghi io.mu. Việc cắt
// có tính idempotent; nếu thất bại thì file gốc được giữ nguyên, lỗi được ném lên tường minh.
func (s *DecisionStore) committedDataUnlocked() ([]byte, error) {
	data, err := s.io.ReadFileUnlocked(decisionsFile)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return data, nil
	}
	keep := bytes.LastIndexByte(data, '\n') + 1
	if err := os.Truncate(s.io.path(decisionsFile), int64(keep)); err != nil {
		return nil, err
	}
	slog.Warn("Đã sửa đuôi chưa commit của kiểm toán phán định",
		"module", "store", "file", decisionsFile, "discarded_bytes", len(data)-keep)
	return data[:keep], nil
}

func parseDecisionRecords(data []byte) ([]DecisionRecord, error) {
	var all []DecisionRecord
	lines := bytes.Split(data, []byte{'\n'})
	for i, raw := range lines {
		if i == len(lines)-1 && len(raw) == 0 {
			break
		}
		var rec DecisionRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, fmt.Errorf("parse %s line %d: %w", decisionsFile, i+1, err)
		}
		all = append(all, rec)
	}
	return all, nil
}

func newDecisionID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("dec-%d", time.Now().UnixNano())
	}
	return "dec-" + hex.EncodeToString(b[:])
}
