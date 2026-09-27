package store

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const checkpointsFile = "meta/checkpoints.jsonl"

// CheckpointStore quản lý việc thêm vào (append) và truy vấn checkpoint cấp step.
// Định dạng trên đĩa: meta/checkpoints.jsonl, chỉ thêm vào cuối; truy vấn đi qua bản
// sao trong bộ nhớ.
// Bất biến: cache là bản sao (mirror) của checkpoints.jsonl, được duy trì tại một điểm
// duy nhất bởi Append/Reset.
// Đồng thời: cache được bảo vệ bởi io.mu, ghi qua Lock, đọc qua RLock.
type CheckpointStore struct {
	io      *IO
	seqGen  atomic.Int64
	cache   []domain.Checkpoint
	loadErr error
	// warnOnce: khi bản sao hỏng, All() chỉ trả về rỗng, nhưng guard chống kẹt và chẩn
	// đoán phụ thuộc vào nó — xuống cấp âm thầm sẽ khiến trạng thái "bị kẹt" hoàn toàn
	// không nhìn thấy (review V-2 MAJOR1), ít nhất để lại một lần Error làm dấu vết.
	warnOnce sync.Once
}

// NewCheckpointStore tạo kho checkpoint, tải một lần các checkpoint có sẵn từ đĩa vào cache.
func NewCheckpointStore(io *IO) *CheckpointStore {
	cs := &CheckpointStore{io: io}
	cs.loadFromDisk()
	return cs
}

// loadFromDisk đọc một lần jsonl trên đĩa vào cache và khôi phục seqGen.
func (cs *CheckpointStore) loadFromDisk() {
	cs.io.mu.Lock()
	defer cs.io.mu.Unlock()

	cs.cache, cs.loadErr = readCheckpointsFile(cs.io.path(checkpointsFile))
	var maxSeq int64
	for _, cp := range cs.cache {
		if cp.Seq > maxSeq {
			maxSeq = cp.Seq
		}
	}
	cs.seqGen.Store(maxSeq)
}

// Append thêm vào một checkpoint.
// Idempotent: nếu đã tồn tại cùng Scope + Step + Digest thì bỏ qua ghi, trả về ngay
// bản ghi đã có.
func (cs *CheckpointStore) Append(scope domain.Scope, step, artifact, digest string) (*domain.Checkpoint, error) {
	cs.io.mu.Lock()
	defer cs.io.mu.Unlock()
	if cs.loadErr != nil {
		return nil, fmt.Errorf("khởi tạo checkpoint store thất bại: %w", cs.loadErr)
	}

	if digest != "" {
		for i := len(cs.cache) - 1; i >= 0; i-- {
			cp := cs.cache[i]
			if cp.Scope.Matches(scope) && cp.Step == step && cp.Digest == digest {
				return &cp, nil
			}
		}
	}

	// Chỉ đẩy seq sau khi ghi thành công, tránh để lại số bị nhảy vĩnh viễn khi ghi lỗi.
	// Đã giữ khoá ghi io.mu nên giữa Load+Store không bị chiếm quyền đồng thời.
	seq := cs.seqGen.Load() + 1
	cp := domain.Checkpoint{
		Seq:        seq,
		Scope:      scope,
		Step:       step,
		Artifact:   artifact,
		Digest:     digest,
		OccurredAt: time.Now(),
	}

	data, err := json.Marshal(cp)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if err := cs.io.AppendLineUnlocked(checkpointsFile, data); err != nil {
		return nil, err
	}
	cs.seqGen.Store(seq)
	cs.cache = append(cs.cache, cp)
	return &cp, nil
}

// AppendArtifact tính dấu vân tay nội dung của artifact rồi mới thêm checkpoint.
func (cs *CheckpointStore) AppendArtifact(scope domain.Scope, step, artifact string) (*domain.Checkpoint, error) {
	if artifact == "" {
		return cs.Append(scope, step, "", "")
	}
	data, err := cs.io.ReadFile(artifact)
	if err != nil {
		return nil, fmt.Errorf("digest artifact %s: %w", artifact, err)
	}
	sum := sha256.Sum256(data)
	return cs.Append(scope, step, artifact, "sha256:"+hex.EncodeToString(sum[:]))
}

// AppendArtifacts tạo dấu vân tay tổ hợp cho nhiều artifact chính thức của cùng một bước.
// Artifact giữ đường dẫn artifact chính đầu tiên; bất kỳ artifact liên quan nào thay đổi
// cũng sẽ tạo ra checkpoint mới.
func (cs *CheckpointStore) AppendArtifacts(scope domain.Scope, step string, artifacts ...string) (*domain.Checkpoint, error) {
	if len(artifacts) == 0 {
		return cs.Append(scope, step, "", "")
	}
	h := sha256.New()
	for _, artifact := range artifacts {
		data, err := cs.io.ReadFile(artifact)
		if err != nil {
			return nil, fmt.Errorf("digest artifact %s: %w", artifact, err)
		}
		_, _ = h.Write([]byte(artifact))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(data)
		_, _ = h.Write([]byte{0})
	}
	return cs.Append(scope, step, artifacts[0], "sha256:"+hex.EncodeToString(h.Sum(nil)))
}

// Latest trả về checkpoint mới nhất của scope chỉ định.
func (cs *CheckpointStore) Latest(scope domain.Scope) *domain.Checkpoint {
	cs.io.mu.RLock()
	defer cs.io.mu.RUnlock()
	for i := len(cs.cache) - 1; i >= 0; i-- {
		if cs.cache[i].Scope.Matches(scope) {
			cp := cs.cache[i]
			return &cp
		}
	}
	return nil
}

// LatestByStep trả về checkpoint mới nhất của scope + step chỉ định.
func (cs *CheckpointStore) LatestByStep(scope domain.Scope, step string) *domain.Checkpoint {
	cs.io.mu.RLock()
	defer cs.io.mu.RUnlock()
	for i := len(cs.cache) - 1; i >= 0; i-- {
		cp := cs.cache[i]
		if cp.Scope.Matches(scope) && cp.Step == step {
			return &cp
		}
	}
	return nil
}

// LatestGlobal trả về checkpoint mới nhất toàn cục (không phân biệt scope).
func (cs *CheckpointStore) LatestGlobal() *domain.Checkpoint {
	cs.io.mu.RLock()
	defer cs.io.mu.RUnlock()
	if len(cs.cache) == 0 {
		return nil
	}
	cp := cs.cache[len(cs.cache)-1]
	return &cp
}

// All trả về bản sao toàn bộ danh sách checkpoint (tăng dần theo seq).
func (cs *CheckpointStore) All() []domain.Checkpoint {
	cs.io.mu.RLock()
	defer cs.io.mu.RUnlock()
	if cs.loadErr != nil {
		cs.warnOnce.Do(func() {
			slog.Error("Không đọc được bản sao checkpoints; guard chống kẹt và chẩn đoán thoái hoá thành không tín hiệu (cách sửa: xoá hoặc sửa meta/checkpoints.jsonl)",
				"module", "store", "file", checkpointsFile, "err", cs.loadErr)
		})
		return nil
	}
	if len(cs.cache) == 0 {
		return nil
	}
	out := make([]domain.Checkpoint, len(cs.cache))
	copy(out, cs.cache)
	return out
}

// Reset xoá trống file checkpoint và cache. Chỉ dùng khi tạo tiểu thuyết mới.
// Xoá file trước rồi mới dọn bộ nhớ: nếu xoá file lỗi thì giữ lại cache và seqGen,
// tránh trạng thái bộ nhớ và đĩa bị lệch nhau.
func (cs *CheckpointStore) Reset() error {
	cs.io.mu.Lock()
	defer cs.io.mu.Unlock()
	if err := cs.io.RemoveFileUnlocked(checkpointsFile); err != nil {
		return err
	}
	cs.seqGen.Store(0)
	cs.cache = nil
	cs.loadErr = nil
	return nil
}

// InitError trả về lỗi tải bản sao checkpoint lúc dựng. Store.Init phải kiểm tra nó
// trước, tránh jsonl hỏng bị diễn giải thành "không có checkpoint".
func (cs *CheckpointStore) InitError() error {
	cs.io.mu.RLock()
	defer cs.io.mu.RUnlock()
	return cs.loadErr
}

// readCheckpointsFile phân tích jsonl nghiêm ngặt; đuôi bị cắt cụt cũng là lỗi bền
// vững cần cho người dùng thấy được.
func readCheckpointsFile(path string) ([]domain.Checkpoint, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var result []domain.Checkpoint
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var cp domain.Checkpoint
		if err := json.Unmarshal(raw, &cp); err != nil {
			return nil, fmt.Errorf("parse %s line %d: %w", checkpointsFile, lineNo, err)
		}
		result = append(result, cp)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", checkpointsFile, err)
	}
	return result, nil
}
