package host

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/revision"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func upgradeProject(st *storepkg.Store) error {
	version, err := st.LoadProjectFormatVersion()
	if err != nil {
		return fmt.Errorf("Đọc phiên bản định dạng dự án: %w", err)
	}
	if version > storepkg.CurrentProjectFormatVersion {
		return fmt.Errorf("Phiên bản định dạng dự án v%d cao hơn v%d mà chương trình hiện tại hỗ trợ, vui lòng nâng cấp ainovel-cli", version, storepkg.CurrentProjectFormatVersion)
	}
	for version < storepkg.CurrentProjectFormatVersion {
		next := version + 1
		switch version {
		case storepkg.LegacyProjectFormatVersion:
			if err := migrateLegacyBook(st); err != nil {
				return fmt.Errorf("Nâng cấp dữ liệu dự án v%d→v%d: %w", version, next, err)
			}
			if err := revision.MigrateLegacyBaseline(st); err != nil {
				return fmt.Errorf("Nâng cấp dữ liệu dự án v%d→v%d: %w", version, next, err)
			}
		default:
			return fmt.Errorf("Không hỗ trợ nâng cấp từ định dạng dự án v%d", version)
		}
		if err := st.SaveProjectFormatVersion(next); err != nil {
			return fmt.Errorf("Lưu phiên bản định dạng dự án v%d: %w", next, err)
		}
		slog.Info("Nâng cấp dữ liệu dự án hoàn tất", "module", "migration", "from", version, "to", next)
		version = next
	}
	return nil
}

func migrateLegacyBook(st *storepkg.Store) error {
	book, err := st.Book.Load()
	if err != nil {
		return err
	}
	if book == nil {
		book, err = loadLegacyBook(st)
		if err != nil || book == nil {
			return err
		}
	}
	if err := st.Book.Save(*book); err != nil {
		return fmt.Errorf("Lưu thông tin tác phẩm cũ: %w", err)
	}
	if _, err := st.Checkpoints.AppendArtifact(domain.GlobalScope(), "book", "meta/book.json"); err != nil {
		return fmt.Errorf("Ghi thông tin tác phẩm cũ: %w", err)
	}
	return nil
}

func loadLegacyBook(st *storepkg.Store) (*domain.BookMetadata, error) {
	data, err := os.ReadFile(filepath.Join(st.Dir(), "meta", "progress.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("Đọc tiến độ tác phẩm cũ: %w", err)
	}
	var legacy struct {
		NovelName string `json:"novel_name"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("Parse tiến độ tác phẩm cũ: %w", err)
	}
	legacy.NovelName = strings.TrimSpace(legacy.NovelName)
	if legacy.NovelName == "" {
		return nil, nil
	}
	premise, err := st.Outline.LoadPremise()
	if err != nil {
		return nil, fmt.Errorf("Đọc premise truyện cũ: %w", err)
	}
	title := legacyPremiseTitle(premise)
	if title == "" {
		return nil, fmt.Errorf("Premise truyện cũ thiếu tiêu đề tên sách")
	}
	if title != legacy.NovelName {
		return nil, fmt.Errorf("Xung đột tên sách tác phẩm cũ: progress=%q, premise=%q", legacy.NovelName, title)
	}
	synopsis := legacyPremiseSection(premise, "核心冲突")
	if synopsis == "" {
		return nil, fmt.Errorf("Premise truyện cũ thiếu mục '核心冲突' (xung đột cốt lõi), không thể tạo tóm tắt tác phẩm")
	}
	return &domain.BookMetadata{Title: title, Synopsis: synopsis}, nil
}

func legacyPremiseTitle(premise string) string {
	for _, line := range strings.Split(premise, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "# ")), "《》\"")
		}
	}
	return ""
}

func legacyPremiseSection(premise, heading string) string {
	var body []string
	matched := false
	for _, line := range strings.Split(premise, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			if matched {
				break
			}
			matched = strings.TrimSpace(strings.TrimPrefix(trimmed, "## ")) == heading
			continue
		}
		if matched {
			body = append(body, line)
		}
	}
	return strings.TrimSpace(strings.Join(body, "\n"))
}

// resumeLabel sinh nhãn UI cho Resume dựa trên dữ kiện.
// label rỗng nghĩa là không có trạng thái khôi phục được (nên đi đường tạo mới). Bản thân việc khôi phục không cần prompt nào —
// Engine chỉ khôi phục dữ kiện: tính lại route từ store rồi chạy tiếp (docs/engine-rfc.md §6).
func resumeLabel(store *storepkg.Store) (string, error) {
	progress, err := store.Progress.Load()
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if progress == nil || progress.Phase == domain.PhaseComplete {
		return "", nil
	}
	return describeResume(store, progress)
}

// describeResume sinh nhãn khôi phục dễ đọc cho người; không ảnh hưởng route của Engine.
// Mọi route thực thi do Flow Router suy theo dữ kiện; đây chỉ hướng UI "Khôi phục: xxx".
func describeResume(store *storepkg.Store, progress *domain.Progress) (string, error) {
	switch progress.Phase {
	case domain.PhasePremise, domain.PhaseOutline:
		return fmt.Sprintf("Khôi phục: giai đoạn lập dàn ý (%s)", progress.Phase), nil
	case domain.PhaseWriting:
		// Ưu tiên khớp với thứ tự ưu tiên quyết định của Router, để label nhất quán với lệnh sắp được phân phát.
		pending, err := store.Signals.LoadPendingCommit()
		if err != nil {
			return "", fmt.Errorf("Đọc lần nộp đang khôi phục: %w", err)
		}
		if pending != nil {
			return fmt.Sprintf("Khôi phục: chương %d nộp dở", pending.Chapter), nil
		}
		if len(progress.PendingRewrites) > 0 {
			verb := "Viết lại"
			if progress.Flow == domain.FlowPolishing {
				verb = "Đánh bóng"
			}
			return fmt.Sprintf("%s khôi phục: %d chương đang chờ xử lý", verb, len(progress.PendingRewrites)), nil
		}
		if progress.Flow == domain.FlowReviewing {
			return "Khôi phục: xem xét dở", nil
		}
		if progress.InProgressChapter > 0 {
			return fmt.Sprintf("Khôi phục: chương %d đang thực hiện", progress.InProgressChapter), nil
		}
		label, err := describeArcEndLabel(store, progress)
		if err != nil {
			return "", err
		}
		if label != "" {
			return label, nil
		}
		return fmt.Sprintf("Khôi phục: viết tiếp từ chương %d", progress.NextChapter()), nil
	}
	return "Khôi phục", nil
}

// describeArcEndLabel sinh nhãn hợp UI cho các trạng thái trung gian cuối cung/cuối quyển.
// Giữ cùng thứ tự với nhánh cuối cung của flow.Route, đảm bảo label khớp với lệnh đầu tiên của Router.
func describeArcEndLabel(store *storepkg.Store, progress *domain.Progress) (string, error) {
	if !progress.Layered || len(progress.CompletedChapters) == 0 {
		return "", nil
	}
	lastCh := progress.CompletedChapters[len(progress.CompletedChapters)-1]
	boundary, err := store.Outline.CheckArcBoundary(lastCh)
	if err != nil {
		return "", fmt.Errorf("Kiểm tra biên cung: %w", err)
	}
	if boundary == nil || !boundary.IsArcEnd {
		return "", nil
	}
	vol, arc := boundary.Volume, boundary.Arc
	hasArcReview, err := store.World.HasArcReview(lastCh)
	if err != nil {
		return "", fmt.Errorf("Đọc xem xét cung: %w", err)
	}
	hasArcSummary, err := store.Summaries.HasArcSummary(vol, arc)
	if err != nil {
		return "", fmt.Errorf("Đọc tóm tắt cung: %w", err)
	}
	hasVolumeSummary := false
	if boundary.IsVolumeEnd {
		hasVolumeSummary, err = store.Summaries.HasVolumeSummary(vol)
		if err != nil {
			return "", fmt.Errorf("Đọc tóm tắt quyển: %w", err)
		}
	}
	switch {
	case !hasArcReview:
		return fmt.Sprintf("Khôi phục: xem xét cuối cung đang chờ xử lý (V%d A%d)", vol, arc), nil
	case !hasArcSummary:
		return fmt.Sprintf("Khôi phục: tóm tắt cung chờ sinh (V%d A%d)", vol, arc), nil
	case boundary.IsVolumeEnd && !hasVolumeSummary:
		return fmt.Sprintf("Khôi phục: tóm tắt quyển chờ sinh (V%d)", vol), nil
	case boundary.NeedsExpansion && boundary.NextArc > 0:
		return fmt.Sprintf("Khôi phục: chờ triển khai cung tiếp theo (V%d A%d)", boundary.NextVolume, boundary.NextArc), nil
	case boundary.NeedsNewVolume:
		return fmt.Sprintf("Khôi phục: chờ quyết định quyển tiếp theo (cuối V%d)", vol), nil
	}
	return "", nil
}
