// Package eval là harness đánh giá offline của ainovel-cli.
//
// Điểm tựa thiết kế: các bộ đánh giá (chẩn đoán xác định diag, văn phong toàn thư stylestat,
// rubric bảy chiều) đã tồn tại sẵn trong dự án, eval chỉ làm một lớp mỏng — điều khiển case
// theo lô, thu thập sản phẩm, ánh xạ diag Finding và hợp đồng case thành cổng, tổng hợp báo cáo.
// Một định nghĩa sự thật, không viết lại phần phán đoán ở tầng đánh giá. Chi tiết xem
// docs/evaluation-system.md.
//
// Hiện đã phủ tuyến chính xác định: cổng đơn đường, delta A/B baseline/variant, tổng hợp repeat
// và hồi quy stylestat. LLM Judge vẫn là lớp tuỳ chọn phía sau, không được làm bẩn cổng xác định.
package eval

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// caseIDPattern giới hạn case id vào ký tự an toàn: id sẽ được ghép vào thư mục xuất và bị
// RemoveAll của RunCase dọn sạch, cấm các ký tự đường dẫn như . / , triệt để "../" băng
// đường dẫn xoá ra ngoài workspace.
var caseIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

const defaultDeltaRatio = 0.3

// Case là một mẫu đánh giá: một yêu cầu sáng tác + một nhóm assertion ở tầng sự thật.
type Case struct {
	ID            string   `json:"id"`
	Category      string   `json:"category"`       // tầng đánh giá: smoke/workflow/quality/longform/recovery/steering
	Role          string   `json:"role,omitempty"` // vai trò được đo: writer/architect/editor (trực giao với Category)
	Description   string   `json:"description,omitempty"`
	Prompt        string   `json:"prompt"`                   // yêu cầu sáng tác của người dùng
	Style         string   `json:"style,omitempty"`          // ghi đè phong cách trong cấu hình
	MaxChapters   int      `json:"max_chapters"`             // giới hạn trên số chương; 0 nghĩa là chỉ chạy tới khi hoạch định xong (vào writing)
	TargetPrompts []string `json:"target_prompts,omitempty"` // các file prompt case này chủ yếu kiểm chứng (mang tính thông tin)
	Rubric        string   `json:"rubric,omitempty"`         // bảng chấm điểm LLM Judge (bật ở Phase 3)
	Expect        Expect   `json:"expect"`
	Gate          Gate     `json:"gate"`
}

// Expect là assertion hợp đồng cấp case — chỉ khai báo những kỳ vọng liên quan chặt tới case này
// mà các quy tắc chung của diag không phủ được.
type Expect struct {
	Phase                string   `json:"phase,omitempty"`                  // phase cuối kỳ vọng
	MinCompletedChapters int      `json:"min_completed_chapters,omitempty"` // số chương hoàn thành tối thiểu
	RequiredCheckpoints  []string `json:"required_checkpoints,omitempty"`   // có dạng "chapter:1:commit" / "arc:1:1:arc_summary" / "global:layered_outline"
	NoPending            []string `json:"no_pending,omitempty"`             // tín hiệu phải dọn sạch khi kết thúc: pending_commit/pending_steer/last_commit/last_review
}

// Gate là ngưỡng cổng của case này. Giai đoạn này chỉ dùng MaxSeverity; các trường còn lại dự
// phòng cho giai đoạn A/B (regression), có phân tích nhưng không tham gia cổng — giữ lại để file
// case viết được theo schema đầy đủ trong docs/evaluation-system.md.
type Gate struct {
	MaxSeverity string `json:"max_severity,omitempty"` // mức nghiêm trọng cao nhất được phép của diag Finding (mặc định warning): vượt quá là hard fail

	MaxCostDeltaRatio     *float64 `json:"max_cost_delta_ratio,omitempty"`
	MaxToolCallDeltaRatio *float64 `json:"max_tool_call_delta_ratio,omitempty"`
	StylestatRegression   string   `json:"stylestat_regression,omitempty"`
}

// Validate kiểm tra các trường bắt buộc của case.
func (c *Case) Validate() error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("case thiếu id")
	}
	if !caseIDPattern.MatchString(c.ID) {
		return fmt.Errorf("case id bất hợp pháp %q: chỉ cho phép chữ thường/số/gạch dưới/gạch nối, và không chứa ký tự đường dẫn", c.ID)
	}
	if strings.TrimSpace(c.Prompt) == "" {
		return fmt.Errorf("case %q thiếu prompt", c.ID)
	}
	if c.Gate.MaxSeverity == "" {
		c.Gate.MaxSeverity = "warning"
	}
	if !validSeverity(c.Gate.MaxSeverity) {
		return fmt.Errorf("gate.max_severity của case %q bất hợp pháp: %s", c.ID, c.Gate.MaxSeverity)
	}
	if c.Gate.MaxCostDeltaRatio == nil {
		c.Gate.MaxCostDeltaRatio = float64Ptr(defaultDeltaRatio)
	}
	if c.Gate.MaxToolCallDeltaRatio == nil {
		c.Gate.MaxToolCallDeltaRatio = float64Ptr(defaultDeltaRatio)
	}
	if c.Gate.StylestatRegression == "" {
		c.Gate.StylestatRegression = "warn"
	}
	if !validStylestatGate(c.Gate.StylestatRegression) {
		return fmt.Errorf("gate.stylestat_regression của case %q bất hợp pháp: %s", c.ID, c.Gate.StylestatRegression)
	}
	return nil
}

func float64Ptr(v float64) *float64 { return &v }

func validStylestatGate(s string) bool {
	switch s {
	case "warn", "block", "off":
		return true
	default:
		return false
	}
}

// LoadCases nạp case từ một file .json đơn lẻ hoặc một thư mục. Toàn bộ *.json trong thư mục
// được nạp đệ quy, sắp xếp theo id.
func LoadCases(path string) ([]Case, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	var files []string
	if info.IsDir() {
		err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(p, ".json") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		files = []string{path}
	}

	var cases []Case
	seen := map[string]string{}
	for _, f := range files {
		c, err := loadCaseFile(f)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[c.ID]; dup {
			return nil, fmt.Errorf("case id trùng lặp: %q (%s với %s)", c.ID, prev, f)
		}
		seen[c.ID] = f
		cases = append(cases, c)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("không tìm thấy case nào: %s", path)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	return cases, nil
}

func loadCaseFile(path string) (Case, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Case{}, err
	}
	var c Case
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields() // gõ sai tên trường là báo lỗi ngay, tránh bỏ qua âm thầm
	if err := dec.Decode(&c); err != nil {
		return Case{}, fmt.Errorf("phân tích case %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Case{}, err
	}
	return c, nil
}
