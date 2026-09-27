package imp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// workspaceSchemaVersion là phiên bản schema tổng thể của workspace nhập.
// Không khớp thì yêu cầu rõ ràng dùng phiên bản khớp hoặc nhập lại, không đoán di trú (RFC §6.1).
const workspaceSchemaVersion = 1

// Digest tính tóm tắt nội dung, theo quy ước sẵn có của repo "sha256:"+hex (xem store/checkpoints.go).
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Artifact là danh tính thống nhất của mỗi artifact ngữ nghĩa trong workspace: phiên bản schema
// + tóm tắt đầu vào + payload. Chỉ khi dựng lại được cùng InputDigest từ đầu vào ngữ nghĩa thật
// hiện tại mới được tái sử dụng (RFC §6.3 / bất biến 1). Không cài dependency graph: LoadState so
// InputDigest từng bước dọc pipeline tuyến tính cố định để quyết định tái sử dụng hay vô hiệu,
// NextAction suy bước kế tiếp từ đó.
type Artifact[T any] struct {
	SchemaVersion int    `json:"schema_version"`
	InputDigest   string `json:"input_digest"`
	Payload       T      `json:"payload"`
}

// Manifest ứng một-đột-một với snapshot nguồn đã chuẩn hóa, là danh tính workspace chứ không phải
// artifact phái sinh (RFC §6.1). Không lưu đường dẫn nguồn tuyệt đối, tránh lộ thư mục máy và
// loại bỏ vấn đề khôi phục khi di chuyển tệp.
type Manifest struct {
	Version          int    `json:"version"`
	SourceName       string `json:"source_name"`
	RawSHA256        string `json:"raw_sha256"`
	NormalizedSHA256 string `json:"normalized_sha256"`
	Encoding         string `json:"encoding"`
	SizeBytes        int64  `json:"size_bytes"`
	CreatedAt        string `json:"created_at"`
}

// Intent lưu cấp phép rõ ràng của người dùng khi khởi động nhập, sau khi khôi phục vẫn phải
// tuân theo, không do artifact đoán ra, Runner không tự ý sửa (RFC §6.1).
type Intent struct {
	Version             int    `json:"version"`
	AutoConfirm         bool   `json:"auto_confirm,omitempty"`
	StoryResolution     string `json:"story_resolution,omitempty"` // open / closed
	ContinueAfterImport bool   `json:"continue_after_import,omitempty"`
}

// Đường dẫn tương đối chuẩn của các artifact trong workspace.
const (
	fileManifest     = "manifest.json"
	fileIntent       = "intent.json"
	fileSource       = "source.txt"
	fileGuidance     = "guidance.txt"
	fileSegmentation = "segmentation.json"
	fileConfirmation = "confirmation.json"
	fileSynthesis    = "synthesis.json"
	fileStoryResolve = "story-resolution.json"
	dirAnalyses      = "analyses"
	dirRangeDigests  = "range-digests"
	dirSegmentChunks = "segment-chunks"
	dirFailures      = "failures"
)

// Workspace là handle đọc ghi artifact nguyên tử của thư mục <gốc sách>/meta/import/.
type Workspace struct {
	dir string
}

// OpenWorkspace trả về handle trỏ tới meta/import/ dưới gốc sách; không bảo đảm thư mục đã
// tồn tại, dùng Active() để kiểm tra.
func OpenWorkspace(bookDir string) *Workspace {
	return &Workspace{dir: filepath.Join(bookDir, "meta", "import")}
}

// Dir trả về đường dẫn tuyệt đối của workspace (dùng cho chẩn đoán và điểm ghi artifact thất bại).
func (w *Workspace) Dir() string { return w.dir }

func (w *Workspace) path(rel string) string { return filepath.Join(w.dir, rel) }

// Active kiểm tra đã có workspace hoạt động được công bố chưa. meta/import/ không tồn tại thì
// không tính là hoạt động; thư mục nửa vời khởi tạo tồn tại dưới dạng meta/import.init-*,
// không bị nhầm thành hoạt động (RFC §6.1).
func (w *Workspace) Active() bool {
	fi, err := os.Stat(w.dir)
	return err == nil && fi.IsDir()
}

func (w *Workspace) has(rel string) bool {
	_, err := os.Stat(w.path(rel))
	return err == nil
}

// writeAtomic ghi nguyên tử rel (so với workspace) theo kiểu "tệp tạm + fsync + rename".
func (w *Workspace) writeAtomic(rel string, data []byte) error {
	full := w.path(rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), filepath.Base(full)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, full); err != nil {
		return err
	}
	syncDir(filepath.Dir(full))
	return nil
}

// syncDir fsync mục thư mục best-effort, giúp rename vừa xong vẫn bền sau mất điện.
// Windows v.v. có thể không hỗ trợ Sync thư mục, bỏ qua lỗi của nó — an toàn khi tiến trình sập
// không phụ thuộc vào nó, chỉ phục vụ thêm kịch bản mất điện (RFC §12.3).
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

func (w *Workspace) writeJSON(rel string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return w.writeAtomic(rel, append(data, '\n'))
}

func (w *Workspace) readJSON(rel string, v any) error {
	data, err := os.ReadFile(w.path(rel))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// LoadManifest đọc danh tính snapshot nguồn của workspace.
func (w *Workspace) LoadManifest() (*Manifest, error) {
	var m Manifest
	if err := w.readJSON(fileManifest, &m); err != nil {
		return nil, err
	}
	if m.Version != workspaceSchemaVersion {
		return nil, fmt.Errorf("phiên bản schema manifest %d != %d, hãy dùng phiên bản khớp để tiếp tục hoặc nhập lại", m.Version, workspaceSchemaVersion)
	}
	return &m, nil
}

// LoadIntent đọc cấp phép người dùng lúc khởi động.
func (w *Workspace) LoadIntent() (*Intent, error) {
	var in Intent
	if err := w.readJSON(fileIntent, &in); err != nil {
		return nil, err
	}
	return &in, nil
}

// LoadSource đọc văn bản snapshot nguồn đã chuẩn hóa.
func (w *Workspace) LoadSource() ([]byte, error) {
	return os.ReadFile(w.path(fileSource))
}

// LoadGuidance đọc hướng dẫn phân tách của người dùng (RFC §18.3); thiếu nghĩa là không có hướng dẫn.
// Hướng dẫn cùng source.txt đều là đầu vào ngữ nghĩa của phân tách chứ không phải artifact phái sinh,
// được cập nhật bằng --guide rõ ràng; nội dung thay đổi làm segmentation và mọi InputDigest hạ
// nguồn tự nhiên mất khớp.
func (w *Workspace) LoadGuidance() (string, error) {
	data, err := os.ReadFile(w.path(fileGuidance))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// readBytes đọc bytes gốc của artifact, dùng để ràng buộc InputDigest hạ nguồn.
func (w *Workspace) readBytes(rel string) ([]byte, error) {
	return os.ReadFile(w.path(rel))
}

// writeArtifact ghi artifact ngữ nghĩa kèm danh tính thống nhất.
func writeArtifact[T any](w *Workspace, rel, inputDigest string, payload T) error {
	return w.writeJSON(rel, Artifact[T]{
		SchemaVersion: workspaceSchemaVersion,
		InputDigest:   inputDigest,
		Payload:       payload,
	})
}

// readArtifact đọc artifact ngữ nghĩa và kiểm tra phiên bản schema; InputDigest có khớp hay không
// do bên gọi phán theo đầu vào hiện tại.
func readArtifact[T any](w *Workspace, rel string) (*Artifact[T], error) {
	var a Artifact[T]
	if err := w.readJSON(rel, &a); err != nil {
		return nil, err
	}
	if a.SchemaVersion != workspaceSchemaVersion {
		return nil, fmt.Errorf("%s phiên bản schema %d != %d, hãy dùng phiên bản khớp để tiếp tục hoặc nhập lại", rel, a.SchemaVersion, workspaceSchemaVersion)
	}
	return &a, nil
}

// clearDir xóa một thư mục đệm trung gian trong workspace. Lỗi phải giao bên gọi xử lý: nuốt lỗi
// sẽ khiến văn bản "đã xóa" nói dối — lần chạy lại sau vẫn dùng lại cache hỏng (antivirus/đối tượng
// bị chiếm trên Windows là kịch bản thật, Debug-First).
func (w *Workspace) clearDir(rel string) error {
	return os.RemoveAll(w.path(rel))
}

// FailureMeta là metadata chẩn đoán của lần thất bại gần nhất (RFC §14.2).
type FailureMeta struct {
	Stage         string `json:"stage"`
	Detail        string `json:"detail"`
	StopReason    string `json:"stop_reason,omitempty"`
	PrefixSalvage string `json:"prefix_salvage,omitempty"` // available:N / unavailable
}

// writeFailure best-effort lưu metadata của lần thất bại gần nhất và phản hồi mô hình gốc chưa
// cắt gọt vào failures/ (RFC §14.2). Phản hồi gốc có thể chứa chính văn, chỉ rơi vào thư mục sách
// của chính người dùng, không vào log thường hay xuất chẩn đoán đã redact.
func (w *Workspace) writeFailure(meta FailureMeta, rawResponse string) {
	_ = w.writeJSON(filepath.Join(dirFailures, "last.json"), meta)
	_ = w.writeAtomic(filepath.Join(dirFailures, "last-response.txt"), []byte(rawResponse))
}

// createWorkspace ghi đủ manifest/intent/source trong thư mục tạm và kiểm tra xong, rồi bằng
// rename thư mục công bố nguyên tử thành meta/import/. Như vậy bộ ba khởi đầu không đi vào
// NextAction ở dạng nửa vời khởi tạo, cũng không cần stage=initializing (RFC §6.1).
func createWorkspace(bookDir string, m Manifest, in Intent, normalized []byte) (*Workspace, error) {
	base := filepath.Join(bookDir, "meta")
	final := filepath.Join(base, "import")
	if fi, err := os.Stat(final); err == nil && fi.IsDir() {
		return nil, fmt.Errorf("workspace nhập đã tồn tại: %s (dùng /import không tham số để khôi phục từ đó)", final)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(base, "import.init-*")
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(tmp)
		}
	}()

	tw := &Workspace{dir: tmp}
	if err := tw.writeAtomic(fileSource, normalized); err != nil {
		return nil, err
	}
	if err := tw.writeJSON(fileManifest, m); err != nil {
		return nil, err
	}
	if err := tw.writeJSON(fileIntent, in); err != nil {
		return nil, err
	}
	// Trước khi công bố, kiểm tra bộ ba đọc được và snapshot nguồn khớp với manifest, loại trừ workspace nửa ghi.
	got, err := tw.LoadManifest()
	if err != nil {
		return nil, fmt.Errorf("kiểm tra manifest khởi tạo: %w", err)
	}
	src, err := tw.LoadSource()
	if err != nil {
		return nil, fmt.Errorf("kiểm tra snapshot nguồn khởi tạo: %w", err)
	}
	if d := Digest(src); d != got.NormalizedSHA256 {
		return nil, fmt.Errorf("tóm tắt snapshot nguồn khởi tạo không khớp: %s != %s", d, got.NormalizedSHA256)
	}
	if _, err := tw.LoadIntent(); err != nil {
		return nil, fmt.Errorf("kiểm tra intent khởi tạo: %w", err)
	}

	if err := os.Rename(tmp, final); err != nil {
		return nil, err
	}
	syncDir(base)
	committed = true
	return &Workspace{dir: final}, nil
}
