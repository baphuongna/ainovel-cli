package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
)

// ErrOutlineChapterNotFound cho biết chương chưa nằm trong dàn ý hiện tại.
var ErrOutlineChapterNotFound = errors.New("outline chapter not found")

// OutlineStore quản lý tiền đề truyện, dàn ý (phẳng/phân tầng) và la bàn.
type OutlineStore struct{ io *IO }

func NewOutlineStore(io *IO) *OutlineStore { return &OutlineStore{io: io} }

// SavePremise lưu tiền đề truyện vào premise.md.
func (s *OutlineStore) SavePremise(content string) error {
	return s.io.WriteMarkdown("premise.md", content)
}

// LoadPremise đọc premise.md. Không tồn tại thì trả về chuỗi rỗng.
func (s *OutlineStore) LoadPremise() (string, error) {
	data, err := s.io.ReadFile("premise.md")
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(data), err
}

// SaveOutline đồng thời lưu outline.json và outline.md (ghi nguyên tử).
func (s *OutlineStore) SaveOutline(entries []domain.OutlineEntry) error {
	return s.io.WithWriteLock(func() error {
		return s.saveOutlineUnlocked(entries)
	})
}

func (s *OutlineStore) saveOutlineUnlocked(entries []domain.OutlineEntry) error {
	if err := s.io.WriteJSONUnlocked("outline.json", entries); err != nil {
		return err
	}
	return s.io.WriteMarkdownUnlocked("outline.md", renderOutline(entries, s.io.labels()))
}

// LoadOutline đọc dàn ý có cấu trúc từ outline.json.
func (s *OutlineStore) LoadOutline() ([]domain.OutlineEntry, error) {
	var entries []domain.OutlineEntry
	if err := s.io.ReadJSON("outline.json", &entries); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return entries, nil
}

// GetChapterOutline lấy mục dàn ý của chương chỉ định.
func (s *OutlineStore) GetChapterOutline(chapter int) (*domain.OutlineEntry, error) {
	entries, err := s.LoadOutline()
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].Chapter == chapter {
			return &entries[i], nil
		}
	}
	return nil, fmt.Errorf("%w: chapter %d", ErrOutlineChapterNotFound, chapter)
}

// SaveLayeredOutline coi dàn ý phân tầng là nguồn duy nhất, lưu view phân tầng và
// dựng lại đồng bộ view phẳng dẫn xuất. Bên gọi không cần và cũng không nên tự duy trì
// riêng outline.json/outline.md.
func (s *OutlineStore) SaveLayeredOutline(volumes []domain.VolumeOutline) error {
	return s.io.WithWriteLock(func() error {
		return s.saveLayeredViewsUnlocked(volumes)
	})
}

// LoadLayeredOutline đọc dàn ý phân tầng.
func (s *OutlineStore) LoadLayeredOutline() ([]domain.VolumeOutline, error) {
	var volumes []domain.VolumeOutline
	if err := s.io.ReadJSON("layered_outline.json", &volumes); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return volumes, nil
}

// ClearLayeredOutline dọn các file dàn ý phân tầng.
func (s *OutlineStore) ClearLayeredOutline() error {
	return s.io.WithWriteLock(func() error {
		if err := s.io.RemoveFileUnlocked("layered_outline.json"); err != nil {
			return err
		}
		return s.io.RemoveFileUnlocked("layered_outline.md")
	})
}

// GetChapterFromLayered tìm theo số chương toàn cục trong dàn ý phân tầng.
func (s *OutlineStore) GetChapterFromLayered(chapter int) (*domain.OutlineEntry, error) {
	volumes, err := s.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}
	ch := 1
	for _, v := range volumes {
		for _, a := range v.Arcs {
			for i := range a.Chapters {
				if ch == chapter {
					e := a.Chapters[i]
					e.Chapter = ch
					return &e, nil
				}
				ch++
			}
		}
	}
	return nil, fmt.Errorf("%w: chapter %d in layered outline", ErrOutlineChapterNotFound, chapter)
}

// LocateChapter định vị tập và cung chứa chương theo số chương toàn cục.
func (s *OutlineStore) LocateChapter(chapter int) (volume, arc int, err error) {
	volumes, err := s.LoadLayeredOutline()
	if err != nil {
		return 0, 0, err
	}
	ch := 1
	for _, v := range volumes {
		for _, a := range v.Arcs {
			for range a.Chapters {
				if ch == chapter {
					return v.Index, a.Index, nil
				}
				ch++
			}
		}
	}
	return 0, 0, fmt.Errorf("%w: chapter %d in layered outline", ErrOutlineChapterNotFound, chapter)
}

// ArcBoundary thông tin ranh giới cung.
type ArcBoundary struct {
	IsArcEnd       bool
	IsVolumeEnd    bool
	Volume         int
	Arc            int
	StartChapter   int
	EndChapter     int
	NextVolume     int
	NextArc        int
	NeedsExpansion bool
	NeedsNewVolume bool // cuối tập và layered_outline hiện tại không có tập kế tiếp
}

// HasNextArc cho biết còn cung kế tiếp hay không.
func (b *ArcBoundary) HasNextArc() bool {
	return b.NextVolume > 0 || b.NextArc > 0
}

// CheckArcBoundary kiểm tra một chương có phải là chương cuối của cung/tập hay không.
func (s *OutlineStore) CheckArcBoundary(chapter int) (*ArcBoundary, error) {
	volumes, err := s.LoadLayeredOutline()
	if err != nil || len(volumes) == 0 {
		return nil, err
	}

	type arcPos struct {
		volIdx, arcIdx int
		volume, arc    int
		chInArc        int
		arcLen         int
		arcStart       int
	}

	ch := 1
	var cur *arcPos
	for vi, v := range volumes {
		for ai, a := range v.Arcs {
			arcStart := ch
			for ci := range a.Chapters {
				if ch == chapter {
					cur = &arcPos{
						volIdx:   vi,
						arcIdx:   ai,
						volume:   v.Index,
						arc:      a.Index,
						chInArc:  ci,
						arcLen:   len(a.Chapters),
						arcStart: arcStart,
					}
				}
				ch++
			}
		}
	}
	if cur == nil {
		return nil, nil
	}

	b := &ArcBoundary{
		Volume:       cur.volume,
		Arc:          cur.arc,
		StartChapter: cur.arcStart,
		EndChapter:   cur.arcStart + cur.arcLen - 1,
	}

	isLastChInArc := cur.chInArc == cur.arcLen-1
	isLastArcInVol := cur.arcIdx == len(volumes[cur.volIdx].Arcs)-1

	// Next*/NeedsExpansion/NeedsNewVolume chỉ có ý nghĩa ở cuối cung, nếu không
	// điều phối viên sẽ tưởng rằng cần khai triển cung kế tiếp sớm.
	if !isLastChInArc {
		return b, nil
	}

	b.IsArcEnd = true
	if isLastArcInVol {
		b.IsVolumeEnd = true
	}

	found := false
	for vi := cur.volIdx; vi < len(volumes); vi++ {
		startArc := 0
		if vi == cur.volIdx {
			startArc = cur.arcIdx + 1
		}
		for ai := startArc; ai < len(volumes[vi].Arcs); ai++ {
			b.NextVolume = volumes[vi].Index
			b.NextArc = volumes[vi].Arcs[ai].Index
			b.NeedsExpansion = !volumes[vi].Arcs[ai].IsExpanded()
			found = true
			break
		}
		if found {
			break
		}
	}

	if b.IsVolumeEnd && !found {
		b.NeedsNewVolume = true
	}

	return b, nil
}

// CompletedArcBoundaries trả về các ranh giới cung chi tiết đã hoàn thành theo thứ tự truyện.
func (s *OutlineStore) CompletedArcBoundaries(lastCompleted int) ([]ArcBoundary, error) {
	volumes, err := s.LoadLayeredOutline()
	if err != nil {
		return nil, err
	}
	chapter := 1
	var result []ArcBoundary
	for _, volume := range volumes {
		for arcIndex, arc := range volume.Arcs {
			if len(arc.Chapters) == 0 {
				continue
			}
			start := chapter
			end := start + len(arc.Chapters) - 1
			chapter = end + 1
			if end > lastCompleted {
				return result, nil
			}
			result = append(result, ArcBoundary{
				IsArcEnd: true, IsVolumeEnd: arcIndex == len(volume.Arcs)-1,
				Volume: volume.Index, Arc: arc.Index, StartChapter: start, EndChapter: end,
			})
		}
	}
	return result, nil
}

// expandArcUnlocked phương thức nội bộ, được gọi trong phối hợp xuyên lĩnh vực Store.ExpandArc.
func (s *OutlineStore) expandArcUnlocked(volumeIdx, arcIdx int, expansion domain.ArcExpansion) ([]domain.VolumeOutline, error) {
	if strings.TrimSpace(expansion.Title) == "" {
		return nil, fmt.Errorf("tiêu đề cung không được để trống")
	}
	if strings.TrimSpace(expansion.Goal) == "" {
		return nil, fmt.Errorf("mục tiêu cung không được để trống")
	}
	if len(expansion.Chapters) == 0 {
		return nil, fmt.Errorf("cung được khai triển phải chứa ít nhất một chương")
	}

	var volumes []domain.VolumeOutline
	if err := s.io.ReadJSONUnlocked("layered_outline.json", &volumes); err != nil {
		return nil, fmt.Errorf("load layered_outline: %w", err)
	}
	found := false
	for vi := range volumes {
		if volumes[vi].Index != volumeIdx {
			continue
		}
		for ai := range volumes[vi].Arcs {
			if volumes[vi].Arcs[ai].Index != arcIdx {
				continue
			}
			if volumes[vi].Arcs[ai].IsExpanded() {
				current := domain.ArcExpansion{
					Title:    volumes[vi].Arcs[ai].Title,
					Goal:     volumes[vi].Arcs[ai].Goal,
					Chapters: volumes[vi].Arcs[ai].Chapters,
				}
				if reflect.DeepEqual(current, expansion) {
					// Lần thử lại idempotent vẫn phải ghi lại toàn bộ các view dẫn xuất bên dưới;
					// lần trước có thể chỉ hoàn tất layered_outline.json, chưa kịp ghi flat outline/Markdown.
					found = true
					break
				}
				return nil, fmt.Errorf("arc already expanded: volume=%d, arc=%d", volumeIdx, arcIdx)
			}
			volumes[vi].Arcs[ai].Title = expansion.Title
			volumes[vi].Arcs[ai].Goal = expansion.Goal
			volumes[vi].Arcs[ai].Chapters = expansion.Chapters
			volumes[vi].Arcs[ai].EstimatedChapters = 0
			found = true
			break
		}
		if found {
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("arc not found: volume=%d, arc=%d", volumeIdx, arcIdx)
	}
	if err := s.saveLayeredViewsUnlocked(volumes); err != nil {
		return nil, err
	}
	return volumes, nil
}

// appendVolumeUnlocked phương thức nội bộ, được gọi trong phối hợp xuyên lĩnh vực Store.AppendVolume.
func (s *OutlineStore) appendVolumeUnlocked(vol domain.VolumeOutline) ([]domain.VolumeOutline, error) {
	var volumes []domain.VolumeOutline
	if err := s.io.ReadJSONUnlocked("layered_outline.json", &volumes); err != nil {
		return nil, fmt.Errorf("load layered_outline: %w", err)
	}
	// Bước kế tiếp của AppendVolume là cập nhật Progress. Nếu tiến trình bị gián đoạn
	// giữa trạng thái "dàn ý đã thêm, Progress chưa cập nhật", việc khôi phục sẽ thử lại
	// với cùng một payload bền vững; tập cuối giống hệt nhau phải được coi là idempotent,
	// để lần thử lại cùng tham số tiếp tục bổ sung Progress, thay vì kẹt cứng vĩnh viễn
	// vì Index trùng lặp.
	if len(volumes) == 0 || !reflect.DeepEqual(volumes[len(volumes)-1], vol) {
		if err := validateAppendVolume(volumes, vol); err != nil {
			return nil, err
		}
		volumes = append(volumes, vol)
	}
	// Kể cả khi tập cuối đã tồn tại cũng ghi lại toàn bộ các view dẫn xuất; lần trước
	// có thể bị gián đoạn đúng lúc sau khi layered JSON ghi xuống đĩa, trước khi ghi
	// flat outline/Markdown.
	if err := s.saveLayeredViewsUnlocked(volumes); err != nil {
		return nil, err
	}
	return volumes, nil
}

// saveLayeredViewsUnlocked coi dàn ý phân tầng là nguồn duy nhất, dựng lại thống nhất
// Markdown và view phẳng dẫn xuất của nó. Bên gọi phải giữ khoá ghi của OutlineStore.
func (s *OutlineStore) saveLayeredViewsUnlocked(volumes []domain.VolumeOutline) error {
	domain.RenumberVolumes(volumes)
	if err := s.io.WriteJSONUnlocked("layered_outline.json", volumes); err != nil {
		return err
	}
	if err := s.io.WriteMarkdownUnlocked("layered_outline.md", renderLayeredOutline(volumes, s.io.labels())); err != nil {
		return err
	}
	if err := s.saveOutlineUnlocked(domain.FlattenOutline(volumes)); err != nil {
		return err
	}
	return nil
}

func (s *OutlineStore) reviseFlatTailUnlocked(fromChapter int, replacement []domain.OutlineEntry) ([]domain.OutlineEntry, error) {
	var outline []domain.OutlineEntry
	if err := s.io.ReadJSONUnlocked("outline.json", &outline); err != nil {
		return nil, fmt.Errorf("load outline: %w: %w", errs.ErrStoreRead, err)
	}
	if fromChapter > len(outline)+1 {
		return nil, fmt.Errorf("from_chapter=%d vượt quá cuối dàn ý %d: %w",
			fromChapter, len(outline), errs.ErrToolPrecondition)
	}
	updated := append([]domain.OutlineEntry(nil), outline[:fromChapter-1]...)
	updated = append(updated, replacement...)
	if len(updated) == 0 {
		return nil, fmt.Errorf("dàn ý sau khi sửa đổi không được để trống: %w", errs.ErrToolPrecondition)
	}
	for i := range updated {
		updated[i].Chapter = i + 1
	}
	if err := s.saveOutlineUnlocked(updated); err != nil {
		return nil, fmt.Errorf("save outline: %w: %w", errs.ErrStoreWrite, err)
	}
	return updated, nil
}

func (s *OutlineStore) reviseLayeredTailUnlocked(fromChapter int, replacement []domain.OutlineEntry) ([]domain.VolumeOutline, error) {
	var volumes []domain.VolumeOutline
	if err := s.io.ReadJSONUnlocked("layered_outline.json", &volumes); err != nil {
		return nil, fmt.Errorf("load layered_outline: %w: %w", errs.ErrStoreRead, err)
	}
	if err := reviseLayeredTail(volumes, fromChapter, replacement); err != nil {
		return nil, fmt.Errorf("%w: %w", errs.ErrToolPrecondition, err)
	}
	if err := s.saveLayeredViewsUnlocked(volumes); err != nil {
		return nil, fmt.Errorf("save layered outline: %w: %w", errs.ErrStoreWrite, err)
	}
	return volumes, nil
}

// reviseLayeredTail thay thế đoạn đuôi của cung chứa fromChapter, tính từ chương đó.
// Nếu fromChapter nằm ngay sau cuối dàn ý phẳng hiện tại thì nối vào cung đã khai triển
// cuối cùng.
func reviseLayeredTail(volumes []domain.VolumeOutline, fromChapter int, replacement []domain.OutlineEntry) error {
	chapter := 1
	targetVolume, targetArc, local := -1, -1, -1
	lastVolume, lastArc := -1, -1
	for vi := range volumes {
		for ai := range volumes[vi].Arcs {
			chapters := volumes[vi].Arcs[ai].Chapters
			if len(chapters) == 0 {
				continue
			}
			lastVolume, lastArc = vi, ai
			if fromChapter >= chapter && fromChapter < chapter+len(chapters) {
				targetVolume, targetArc = vi, ai
				local = fromChapter - chapter
				break
			}
			chapter += len(chapters)
		}
		if targetVolume >= 0 {
			break
		}
	}
	if targetVolume < 0 && fromChapter == chapter && lastVolume >= 0 {
		targetVolume, targetArc = lastVolume, lastArc
		local = len(volumes[lastVolume].Arcs[lastArc].Chapters)
	}
	if targetVolume < 0 {
		return fmt.Errorf("from_chapter=%d không nằm trong phạm vi dàn ý đã khai triển", fromChapter)
	}

	arc := &volumes[targetVolume].Arcs[targetArc]
	updated := append([]domain.OutlineEntry(nil), arc.Chapters[:local]...)
	updated = append(updated, replacement...)
	if len(updated) == 0 {
		return fmt.Errorf("cung mục tiêu sau khi sửa đổi không được để trống")
	}
	arc.Chapters = updated
	arc.EstimatedChapters = 0
	return nil
}

func validateAppendVolume(existing []domain.VolumeOutline, vol domain.VolumeOutline) error {
	if len(existing) > 0 {
		maxIdx := existing[len(existing)-1].Index
		if vol.Index <= maxIdx {
			return fmt.Errorf("Index tập %d phải lớn hơn giá trị lớn nhất hiện có %d", vol.Index, maxIdx)
		}
	}
	if len(vol.Arcs) == 0 {
		return fmt.Errorf("tập mới phải chứa ít nhất một cung")
	}
	if !vol.Arcs[0].IsExpanded() {
		return fmt.Errorf("cung đầu của tập mới phải chứa các chương chi tiết")
	}
	return nil
}

// SaveCompass lưu la bàn hướng kết thúc truyện.
func (s *OutlineStore) SaveCompass(compass domain.StoryCompass) error {
	if compass.EndingDirection == "" {
		return fmt.Errorf("ending_direction không được để trống")
	}
	return s.io.WriteJSON("meta/compass.json", compass)
}

// LoadCompass đọc la bàn hướng kết thúc truyện.
func (s *OutlineStore) LoadCompass() (*domain.StoryCompass, error) {
	var c domain.StoryCompass
	if err := s.io.ReadJSON("meta/compass.json", &c); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

// SaveFoundationAudit lưu kết quả xem xét ngữ nghĩa của Architect cho phiên bản thiết
// lập nền tảng hiện tại.
func (s *OutlineStore) SaveFoundationAudit(a domain.FoundationAudit) error {
	return s.io.WriteJSON("meta/foundation_audit.json", a)
}

// LoadFoundationAudit đọc kết quả xem xét ngữ nghĩa thiết lập nền tảng gần nhất.
func (s *OutlineStore) LoadFoundationAudit() (*domain.FoundationAudit, error) {
	var a domain.FoundationAudit
	if err := s.io.ReadJSON("meta/foundation_audit.json", &a); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

func renderLayeredOutline(volumes []domain.VolumeOutline, l mdLabels) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", l.layeredOutline)
	ch := 1
	for _, v := range volumes {
		fmt.Fprintf(&b, "## "+l.volumeFmt+"%s%s\n\n", v.Index, l.colon, v.Title)
		fmt.Fprintf(&b, "**%s**%s%s\n\n", l.theme, l.colon, v.Theme)
		for _, a := range v.Arcs {
			fmt.Fprintf(&b, "### "+l.arcFmt+"%s%s\n\n", a.Index, l.colon, a.Title)
			fmt.Fprintf(&b, "**%s**%s%s\n\n", l.goal, l.colon, a.Goal)
			if !a.IsExpanded() {
				fmt.Fprintf(&b, l.pendingArcFmt+"\n\n", a.EstimatedChapters)
				continue
			}
			for _, e := range a.Chapters {
				fmt.Fprintf(&b, "#### "+l.chapterFmt+"%s%s\n\n", ch, l.colon, e.Title)
				fmt.Fprintf(&b, "**%s**%s%s\n\n", l.coreEvent, l.colon, e.CoreEvent)
				if e.Hook != "" {
					fmt.Fprintf(&b, "**%s**%s%s\n\n", l.hook, l.colon, e.Hook)
				}
				ch++
			}
		}
	}
	return b.String()
}

func renderOutline(entries []domain.OutlineEntry, l mdLabels) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", l.outline)
	for _, e := range entries {
		fmt.Fprintf(&b, "## "+l.chapterFmt+"%s%s\n\n", e.Chapter, l.colon, e.Title)
		fmt.Fprintf(&b, "**%s**%s%s\n\n", l.coreEvent, l.colon, e.CoreEvent)
		if e.Hook != "" {
			fmt.Fprintf(&b, "**%s**%s%s\n\n", l.hook, l.colon, e.Hook)
		}
		if len(e.Scenes) > 0 {
			fmt.Fprintf(&b, "**%s**%s\n", l.scenes, l.colon)
			for i, sc := range e.Scenes {
				fmt.Fprintf(&b, "%d. %s\n", i+1, sc)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// ── Hồ phản hồi dàn ý của Writer ──
//
// feedback (lệch hướng/đề xuất) của commit_chapter được lưu bền vững ở đây, kiến trúc
// sư lần thao tác cấu trúc kế tiếp (expand_arc / append_volume / update_compass) tiêu
// thụ qua novel_context rồi xoá sạch.
// Vòng khép kín dữ kiện: công cụ ghi đĩa → bơm vào ngữ cảnh → thao tác cấu trúc tức
// tiêu thụ (chặn 1 trong docs/engine-arbiter.md).

// ChapterFeedback một phản hồi dàn ý kèm số chương.
type ChapterFeedback struct {
	Chapter          int      `json:"chapter"`
	StoryChanged     bool     `json:"story_changed,omitempty"`
	ChangeSummary    string   `json:"change_summary,omitempty"`
	Deviation        string   `json:"deviation,omitempty"`
	Suggestion       string   `json:"suggestion,omitempty"`
	DownstreamIssues []string `json:"downstream_issues,omitempty"`
	At               string   `json:"at"`
}

// RequiresImmediateReview phân biệt ảnh hưởng của sửa đổi ngoài với phản hồi viết
// thông thường. Phản hồi thông thường để dành cho lần thao tác cấu trúc tự nhiên kế
// tiếp hấp thụ cùng lúc; còn sửa đổi ngoài có thể làm mất hiệu lực dàn ý sắp viết tiếp,
// nên phải chuyển cho Architect trước.
func (f ChapterFeedback) RequiresImmediateReview() bool {
	return f.StoryChanged || strings.TrimSpace(f.ChangeSummary) != "" || len(f.DownstreamIssues) > 0
}

const outlineFeedbackFile = "meta/outline_feedback.jsonl"
const outlineFeedbackResolutionFile = "meta/outline_feedback_resolution.json"

// AppendOutlineFeedback nối thêm một phản hồi của writer. Cùng chương và cùng nội
// dung được coi là một dữ kiện, để khi commit crash trước ProgressMarked thì replay
// không cộng lặp các phản hồi kèm theo.
func (s *OutlineStore) AppendOutlineFeedback(fb ChapterFeedback) error {
	return s.io.WithWriteLock(func() error {
		existing, err := s.io.ReadFileUnlocked(outlineFeedbackFile)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		currentFeedback, err := parseOutlineFeedback(existing)
		if err != nil {
			return err
		}
		for _, current := range currentFeedback {
			if current.Chapter == fb.Chapter && current.StoryChanged == fb.StoryChanged &&
				current.ChangeSummary == fb.ChangeSummary && current.Deviation == fb.Deviation &&
				current.Suggestion == fb.Suggestion && reflect.DeepEqual(current.DownstreamIssues, fb.DownstreamIssues) {
				return nil
			}
		}
		if fb.At == "" {
			fb.At = time.Now().Format(time.RFC3339)
		}
		data, err := json.Marshal(fb)
		if err != nil {
			return err
		}
		return s.io.AppendLineUnlocked(outlineFeedbackFile, append(data, '\n'))
	})
}

// LoadPendingOutlineFeedback đọc các phản hồi chưa tiêu thụ (cũ→mới). Dòng hỏng
// trả về lỗi tường minh, để tránh trường hợp Architect tiếp tục thao tác cấu trúc
// trên ngữ cảnh thiếu một phần phản hồi rồi xoá sạch file gốc.
func (s *OutlineStore) LoadPendingOutlineFeedback() ([]ChapterFeedback, error) {
	s.io.mu.RLock()
	defer s.io.mu.RUnlock()
	data, err := os.ReadFile(s.io.path(outlineFeedbackFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseOutlineFeedback(data)
}

func (s *OutlineStore) SaveOutlineFeedbackResolution(reason string, count int) error {
	return s.io.WriteJSON(outlineFeedbackResolutionFile, struct {
		Reason   string `json:"reason"`
		Resolved int    `json:"resolved"`
		At       string `json:"at"`
	}{Reason: reason, Resolved: count, At: time.Now().Format(time.RFC3339)})
}

func parseOutlineFeedback(data []byte) ([]ChapterFeedback, error) {
	var out []ChapterFeedback
	for lineNo, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var fb ChapterFeedback
		if err := json.Unmarshal([]byte(line), &fb); err != nil {
			return nil, fmt.Errorf("parse %s line %d: %w", outlineFeedbackFile, lineNo+1, err)
		}
		out = append(out, fb)
	}
	return out, nil
}

// ClearOutlineFeedback xoá sạch hồ phản hồi (thao tác cấu trúc của architect thành
// công = phản hồi đã được tham khảo).
func (s *OutlineStore) ClearOutlineFeedback() error {
	s.io.mu.Lock()
	defer s.io.mu.Unlock()
	data, err := os.ReadFile(s.io.path(outlineFeedbackFile))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if _, err := parseOutlineFeedback(data); err != nil {
		return err
	}
	err = os.Remove(s.io.path(outlineFeedbackFile))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
