package eval

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

// Command là điểm vào lệnh con `ainovel-cli eval`, trả mã thoát tiến trình:
// 0=PASS/WARN, 1=có case FAIL, 2=lỗi dùng pháp/cấu hình.
//
// Quy trình rõ ràng: nạp cấu hình → nạp case → chạy theo cách tổ chức single/A-B → thu thập →
// chấm → tổng hợp → báo cáo.
func Command(argv []string) int {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	casesPath := fs.String("cases", "", "thư mục case hoặc một file .json đơn lẻ (bắt buộc)")
	variantDir := fs.String("variant", "", "thư mục ghi đè prompt variant (chứa các prompt cốt lõi như writer.md)")
	configPath := fs.String("config", "", "đường dẫn file cấu hình (mặc định dùng đường dẫn mặc định)")
	outDir := fs.String("out", "", "thư mục xuất báo cáo (mặc định workspace/evals/<run_id>)")
	maxChapters := fs.Int("max-chapters", -1, "ghi đè giới hạn số chương của mọi case (-1=không ghi đè)")
	timeout := fs.Duration("timeout", 30*time.Minute, "giới hạn wall-clock cho một case (0=không giới hạn)")
	repeat := fs.Int("repeat", 1, "số lần chạy lặp lại mỗi case (giảm ảnh hưởng tính ngẫu nhiên của mô hình)")
	ci := fs.Bool("ci", false, "chế độ CI: dìm tiến độ từng sự kiện, chỉ in kết luận cuối (mã thoát đã phản ánh cổng, không cần flag này vẫn có tác dụng)")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	if strings.TrimSpace(*casesPath) == "" {
		fmt.Fprintln(os.Stderr, "eval: thiếu --cases")
		fs.Usage()
		return 2
	}
	if *repeat <= 0 {
		fmt.Fprintln(os.Stderr, "eval: --repeat phải lớn hơn 0")
		return 2
	}

	// Khi -config của eval trỏ tới file độc lập thì nạp theo file đơn (tái tạo được, không bị
	// toàn cục/dự án trên máy làm bẩn); mặc định thì đi qua gộp hai tầng toàn cục + dự án.
	loadConfig := bootstrap.LoadConfig
	if strings.TrimSpace(*configPath) != "" {
		loadConfig = func() (bootstrap.Config, error) { return bootstrap.LoadConfigFile(*configPath) }
	}
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "eval: nạp cấu hình thất bại: %v\n", err)
		return 2
	}

	cases, err := LoadCases(*casesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "eval: nạp case thất bại: %v\n", err)
		return 2
	}

	variantPrompts, err := loadVariant(*variantDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "eval: nạp variant thất bại: %v\n", err)
		return 2
	}

	runID := time.Now().Format("20060102-150405")
	if *outDir == "" {
		*outDir = filepath.Join("workspace", "evals", runID)
	}
	variantName := ""
	if *variantDir != "" {
		variantName = filepath.Base(*variantDir)
	}

	mode := "single"
	if variantName != "" {
		mode = "ab"
	}
	fmt.Fprintf(os.Stderr, "eval run %s · %d cases · mode=%s · variant=%s · repeat=%d\n",
		runID, len(cases), mode, orNone(variantName), *repeat)

	caseResults := make([]CaseResult, 0, len(cases))
	for _, c := range cases {
		if *maxChapters >= 0 {
			c.MaxChapters = *maxChapters
		}
		fmt.Fprintf(os.Stderr, "\n▶ %s (%s)\n", c.ID, c.Category)

		style := c.Style
		if style == "" {
			style = cfg.Style
		}
		var progressW io.Writer
		if !*ci {
			progressW = os.Stderr // chế độ CI dìm xuất từng sự kiện, giữ log sạch
		}

		if variantName == "" {
			runs := make([]RunResult, 0, *repeat)
			for i := 1; i <= *repeat; i++ {
				bundle := assets.Load(style, assets.LoadOptions{}) // thuần nội dựng, baseline xác định, không bị bản ghi đè trên máy làm bẩn
				dir := runDir(*outDir, c.ID, ArmSingle, i, *repeat)
				res := runOne(cfg, bundle, c, dir, *timeout, progressW)
				res.Arm, res.Repeat = ArmSingle, i
				runs = append(runs, RunResult{Arm: ArmSingle, Repeat: i, Result: res})
				fmt.Fprintf(os.Stderr, "  → single#%d %s\n", i, res.Outcome)
			}
			caseResults = append(caseResults, NewSingleRunsCaseResult(c, runs))
			continue
		}

		runs := make([]RunResult, 0, *repeat*2)
		deltas := make([]Delta, 0, *repeat)
		for i := 1; i <= *repeat; i++ {
			baseBundle := assets.Load(style, assets.LoadOptions{})
			baseDir := runDir(*outDir, c.ID, ArmBaseline, i, *repeat)
			base := runOne(cfg, baseBundle, c, baseDir, *timeout, progressW)
			base.Arm, base.Repeat = ArmBaseline, i
			runs = append(runs, RunResult{Arm: ArmBaseline, Repeat: i, Result: base})
			fmt.Fprintf(os.Stderr, "  → baseline#%d %s\n", i, base.Outcome)

			varBundle := assets.Load(style, assets.LoadOptions{})
			if err := applyVariant(&varBundle, variantPrompts); err != nil {
				fmt.Fprintf(os.Stderr, "eval: ghi đè variant thất bại: %v\n", err)
				return 2
			}
			varDir := runDir(*outDir, c.ID, ArmVariant, i, *repeat)
			variant := runOne(cfg, varBundle, c, varDir, *timeout, progressW)
			variant.Arm, variant.Repeat = ArmVariant, i
			runs = append(runs, RunResult{Arm: ArmVariant, Repeat: i, Result: variant})
			delta := GradeDelta(c, base, variant)
			deltas = append(deltas, delta)
			fmt.Fprintf(os.Stderr, "  → variant#%d %s · delta %s\n", i, variant.Outcome, delta.Outcome)
		}
		caseResults = append(caseResults, NewABCaseResult(c, runs, deltas))
	}

	suite := Aggregate(runID, mode, variantName, *repeat, caseResults)
	if err := WriteReport(suite, *outDir); err != nil {
		fmt.Fprintf(os.Stderr, "eval: viết báo cáo thất bại: %v\n", err)
		return 2
	}

	fmt.Fprintf(os.Stderr, "\n%s\nBáo cáo: %s\n", Summary(suite), filepath.Join(*outDir, "report.md"))
	if suite.Gate == Fail {
		return 1
	}
	return 0
}

func runOne(cfg bootstrap.Config, bundle assets.Bundle, c Case, dir string, timeout time.Duration, progressW io.Writer) Result {
	runErr := RunCase(cfg, bundle, c, RunOptions{
		OutputDir: dir,
		Timeout:   timeout,
		Progress:  progressW,
	})
	col := Collect(dir, runErr)
	return Grade(c, col)
}

func runDir(outDir, caseID, arm string, repeat, totalRepeats int) string {
	if totalRepeats <= 1 {
		if arm == ArmSingle {
			return filepath.Join(outDir, "artifacts", caseID)
		}
		return filepath.Join(outDir, "artifacts", caseID, arm)
	}
	if arm == ArmSingle {
		return filepath.Join(outDir, "artifacts", caseID, fmt.Sprintf("r%d", repeat))
	}
	return filepath.Join(outDir, "artifacts", caseID, fmt.Sprintf("r%d", repeat), arm)
}

// loadVariant đọc mọi *.md dưới thư mục variant (tên file → nội dung). Thư mục rỗng trả map rỗng.
func loadVariant(dir string) (map[string]string, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = string(data)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("thư mục variant không có file *.md nào: %s", dir)
	}
	return out, nil
}

func applyVariant(b *assets.Bundle, prompts map[string]string) error {
	for file, raw := range prompts {
		// voice.md là lối vào variant riêng của tầng văn phong: chỉ thay đoạn văn phong, mẫu
		// giao thức giữ nguyên, việc lắp ghép vẫn đi cùng một đường BuildWriterPrompt
		// (docs/voice-layer.md §3.6).
		if file == "voice.md" {
			b.OverrideVoice(raw)
			continue
		}
		if err := b.OverridePrompt(file, raw); err != nil {
			return err
		}
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "<none>"
	}
	return s
}
