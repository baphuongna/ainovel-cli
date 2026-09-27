package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
)

// Store là composite root của quản lý trạng thái, giữ mọi store con.
type Store struct {
	dir string
	ios []*IO

	Progress       *ProgressStore
	Book           *BookStore
	Outline        *OutlineStore
	Drafts         *DraftStore
	Summaries      *SummaryStore
	RunMeta        *RunMetaStore
	UserRules      *UserRulesStore
	Signals        *SignalStore
	Runtime        *RuntimeStore
	Characters     *CharacterStore
	Cast           *CastStore
	World          *WorldStore
	Checkpoints    *CheckpointStore
	Sessions       *SessionStore
	Usage          *UsageStore
	Simulation     *SimulationStore
	Decisions      *DecisionStore
	ChapterRecords *ChapterRecordStore
	Revisions      *RevisionStore

	crossMu sync.Mutex // tuần tự hóa phối hợp liên miền; không có nghĩa nhiều tệp có tính nguyên tử giao dịch
}

const (
	LegacyProjectFormatVersion  = 1
	CurrentProjectFormatVersion = 2
	projectFormatPath           = "meta/format.json"
)

type projectFormat struct {
	Version int `json:"version"`
}

// NewStore tạo trình quản lý trạng thái, dir là thư mục gốc xuất tiểu thuyết.
func NewStore(dir string) *Store {
	var ios []*IO
	// mk ghi lại IO của từng store con: chúng tự giữ khóa riêng (không chặn nhau), nhưng ngôn ngữ
	// là thống nhất toàn sách, chỉ khi đăng ký tập trung mới đặt xong một lần và không bỏ sót store mới.
	mk := func() *IO { x := newIO(dir); ios = append(ios, x); return x }
	io := mk()
	outline := NewOutlineStore(io)
	s := &Store{
		dir:            dir,
		Progress:       NewProgressStore(mk()),
		Book:           NewBookStore(mk()),
		Outline:        outline,
		Drafts:         NewDraftStore(mk()),
		Summaries:      NewSummaryStore(mk(), outline),
		RunMeta:        NewRunMetaStore(mk()),
		UserRules:      NewUserRulesStore(mk()),
		Signals:        NewSignalStore(mk()),
		Runtime:        NewRuntimeStore(mk()),
		Characters:     NewCharacterStore(mk(), outline),
		Cast:           NewCastStore(mk()),
		World:          NewWorldStore(mk()),
		Checkpoints:    NewCheckpointStore(io),
		Sessions:       NewSessionStore(mk()),
		Usage:          NewUsageStore(mk()),
		Simulation:     NewSimulationStore(mk()),
		Decisions:      NewDecisionStore(mk()),
		ChapterRecords: NewChapterRecordStore(mk()),
		Revisions:      NewRevisionStore(mk()),
	}
	s.ios = ios
	return s
}

// SetLanguage đặt ngôn ngữ tác phẩm ("vi" / "zh"), ảnh hưởng nhãn của mọi khung nhìn Markdown
// dẫn xuất. Đặt một lần lúc khởi động là đủ; nếu không gọi thì theo mặc định tiếng Trung của thượng nguồn.
func (s *Store) SetLanguage(lang string) {
	for _, x := range s.ios {
		x.SetLanguage(lang)
	}
}

// Language trả về ngôn ngữ tác phẩm đã cài qua SetLanguage ("vi" / "zh"); rỗng nếu chưa từng cài
// (lúc đó nhãn store theo mặc định tiếng Trung của thượng nguồn). Công cụ commit dùng giá trị này
// để đóng đề mục chương theo ngôn ngữ sách.
func (s *Store) Language() string {
	if len(s.ios) == 0 {
		return ""
	}
	return s.ios[0].lang
}

// Dir trả về thư mục gốc xuất.
func (s *Store) Dir() string { return s.dir }

// LoadProjectFormatVersion trả về phiên bản định dạng dữ liệu của thư mục tác phẩm. Tác phẩm cũ
// không có tệp phiên bản được coi là v1, do migration lúc khởi động nâng cấp thống nhất, code nghiệp
// vụ không cần giữ nhánh định dạng cũ.
func (s *Store) LoadProjectFormatVersion() (int, error) {
	var format projectFormat
	if err := s.Progress.io.ReadJSON(projectFormatPath, &format); err != nil {
		if os.IsNotExist(err) {
			return LegacyProjectFormatVersion, nil
		}
		return 0, err
	}
	if format.Version <= 0 {
		return 0, fmt.Errorf("phiên bản định dạng dự án không hợp lệ: %d", format.Version)
	}
	return format.Version, nil
}

// SaveProjectFormatVersion cập nhật nguyên tử phiên bản định dạng dự án sau khi một migration hoàn tất toàn bộ.
func (s *Store) SaveProjectFormatVersion(version int) error {
	if version <= 0 {
		return fmt.Errorf("phiên bản định dạng dự án phải lớn hơn 0: %d", version)
	}
	return s.Progress.io.WriteJSON(projectFormatPath, projectFormat{Version: version})
}

// CheckConsistency kiểm tra nông một lần tầng sự thực, dùng sinh warning lúc khởi động/khôi phục.
// Thuần chỉ đọc: không sửa dữ liệu, chỉ trả về mô tả vấn đề dễ đọc. Bên gọi quyết định cách hiển thị
// (log / UI). Để tránh chi phí IO quét cả thư mục, chỉ kiểm các điểm then chốt của Progress:
//   - chương hoàn thành cuối cùng phải có chính văn bản cuối trong chapters/
//   - ở chế độ Layered, Volume/Arc hiện tại phải tìm được trong layered_outline
func (s *Store) CheckConsistency() []string {
	var warnings []string
	progress, err := s.Progress.Load()
	if err != nil {
		return append(warnings, fmt.Sprintf("đọc progress thất bại: %v", err))
	}
	if progress == nil {
		return warnings
	}
	if n := len(progress.CompletedChapters); n > 0 {
		lastCh := progress.CompletedChapters[n-1]
		if text, err := s.Drafts.LoadChapterText(lastCh); err != nil {
			warnings = append(warnings, fmt.Sprintf("đọc chính văn bản cuối của chương %d thất bại: %v", lastCh, err))
		} else if text == "" {
			warnings = append(warnings, fmt.Sprintf("progress đánh dấu chương %d đã hoàn thành, nhưng chapters/%02d.md không tồn tại hoặc rỗng", lastCh, lastCh))
		}
	}
	if progress.Layered && progress.CurrentVolume > 0 && progress.CurrentArc > 0 {
		volumes, err := s.Outline.LoadLayeredOutline()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("đọc dàn ý phân tầng thất bại: %v", err))
		} else if len(volumes) > 0 {
			found := false
			for _, v := range volumes {
				if v.Index != progress.CurrentVolume {
					continue
				}
				for _, a := range v.Arcs {
					if a.Index == progress.CurrentArc {
						found = true
						break
					}
				}
				break
			}
			if !found {
				warnings = append(warnings, fmt.Sprintf("progress hiện tại V%d A%d không tìm thấy mục tương ứng trong dàn ý phân tầng", progress.CurrentVolume, progress.CurrentArc))
			}
		}
	}
	return warnings
}

// FoundationMissing trả về thông tin tác phẩm và thiết lập nền tảng còn thiếu trong lập kế hoạch ban
// đầu, thứ tự ổn định. Chế độ trường thiên (đã có layered_outline) yêu cầu thêm compass. Lỗi đọc phải
// trả về nguyên trạng, không được coi artifact hỏng hoặc không đủ quyền đọc là “chưa tạo”, nếu không
// bên gọi có thể ghi đè dữ liệu thật.
func (s *Store) FoundationMissing() ([]string, error) {
	var missing []string
	book, err := s.Book.Load()
	if err != nil {
		return nil, fmt.Errorf("load book metadata: %w", err)
	}
	if book == nil {
		missing = append(missing, "book")
	}
	premise, err := s.Outline.LoadPremise()
	if err != nil {
		return nil, fmt.Errorf("load premise: %w", err)
	}
	if premise == "" {
		missing = append(missing, "premise")
	}
	outline, err := s.Outline.LoadOutline()
	if err != nil {
		return nil, fmt.Errorf("load outline: %w", err)
	}
	if len(outline) == 0 {
		missing = append(missing, "outline")
	}
	characters, err := s.Characters.Load()
	if err != nil {
		return nil, fmt.Errorf("load characters: %w", err)
	}
	if len(characters) == 0 {
		missing = append(missing, "characters")
	}
	rules, err := s.World.LoadWorldRules()
	if err != nil {
		return nil, fmt.Errorf("load world rules: %w", err)
	}
	if len(rules) == 0 {
		missing = append(missing, "world_rules")
	}
	layered, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		return nil, fmt.Errorf("load layered outline: %w", err)
	}
	if len(layered) > 0 {
		compass, err := s.Outline.LoadCompass()
		if err != nil {
			return nil, fmt.Errorf("load compass: %w", err)
		}
		if compass == nil {
			missing = append(missing, "compass")
		}
	}
	// Sách mới chỉ khi đã qua xem xét ngữ nghĩa tường minh của model trên artifact đã lưu đĩa thì mới
	// được đi từ lập kế hoạch sang viết. PhaseWriting/Complete đại diện sách cũ hoặc sách mới đã xem xét,
	// giữ tương thích dự án cũ; bản thân việc xem xét là một hành động chứ không phải tệp thiếu, nên chỉ
	// thêm vào khi các artifact khác đã đủ.
	if len(missing) == 0 {
		progress, err := s.Progress.Load()
		if err != nil {
			return nil, fmt.Errorf("load progress: %w", err)
		}
		if progress == nil || (progress.Phase != domain.PhaseWriting && progress.Phase != domain.PhaseComplete) {
			missing = append(missing, "foundation_audit")
		}
	}
	return missing, nil
}

// FoundationFingerprint trả về dấu vân tay nội dung của artifact thiết lập nền tảng hiện tại.
// Architect phải giao trả nguyên trạng giá trị đọc được từ novel_context cho công cụ xem xét, bảo đảm
// kết luận nhắm đúng phiên bản thực sự lưu đĩa, chứ không phải nội dung chưa lưu hoặc đã cũ trong hội thoại.
func (s *Store) FoundationFingerprint() (string, error) {
	files := []string{"meta/book.json", "premise.md", "outline.json", "characters.json", "world_rules.json"}
	layered, err := s.Outline.LoadLayeredOutline()
	if err != nil {
		return "", fmt.Errorf("load layered outline: %w", err)
	}
	if len(layered) > 0 {
		files = append(files, "layered_outline.json", "meta/compass.json")
	}

	h := sha256.New()
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(s.dir, filepath.FromSlash(rel)))
		if err != nil {
			return "", fmt.Errorf("read %s: %w", rel, err)
		}
		_, _ = h.Write([]byte(rel))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(data)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Init tạo cấu trúc thư mục con cần thiết.
func (s *Store) Init() error {
	if err := s.Checkpoints.InitError(); err != nil {
		// Đưa ra đường dẫn khôi phục khả thi (review V-2.4): khi người dùng bị chặn ngoài cửa thì cần nhất là biết xóa tệp nào.
		return fmt.Errorf("load checkpoints: %w (tệp %s hỏng; xóa hoặc sửa tệp đó thì có thể khôi phục khởi động, chỉ mất bản ghi checkpoint, không ảnh hưởng nội dung chương)",
			err, s.Checkpoints.io.path(checkpointsFile))
	}
	return s.Progress.io.EnsureDirs([]string{
		"chapters", "summaries", "drafts", "reviews", "meta", "meta/chapter_records", "meta/runtime", "meta/runtime/tasks", "meta/sessions", "meta/sessions/agents",
	})
}

// ── Phương thức phối hợp liên miền ──

// ExpandArc hiệu chỉnh cung khung và khai triển thành chương chi tiết (Outline + Progress phối hợp).
func (s *Store) ExpandArc(volumeIdx, arcIdx int, expansion domain.ArcExpansion) error {
	s.crossMu.Lock()
	defer s.crossMu.Unlock()

	s.Outline.io.mu.Lock()
	defer s.Outline.io.mu.Unlock()

	volumes, err := s.Outline.expandArcUnlocked(volumeIdx, arcIdx, expansion)
	if err != nil {
		return err
	}

	s.Progress.io.mu.Lock()
	defer s.Progress.io.mu.Unlock()

	p, err := s.Progress.loadUnlocked()
	if err != nil {
		return err
	}
	if p == nil {
		p = &domain.Progress{}
	}
	p.TotalChapters = domain.EstimatedChapterCapacity(volumes)
	return s.Progress.saveUnlocked(p)
}

// AppendVolume thêm tập mới vào cuối dàn ý phân tầng (Outline + Progress phối hợp).
func (s *Store) AppendVolume(vol domain.VolumeOutline) error {
	s.crossMu.Lock()
	defer s.crossMu.Unlock()

	s.Outline.io.mu.Lock()
	defer s.Outline.io.mu.Unlock()

	volumes, err := s.Outline.appendVolumeUnlocked(vol)
	if err != nil {
		return err
	}

	s.Progress.io.mu.Lock()
	defer s.Progress.io.mu.Unlock()

	p, err := s.Progress.loadUnlocked()
	if err != nil {
		return err
	}
	if p == nil {
		p = &domain.Progress{}
	}
	p.TotalChapters = domain.EstimatedChapterCapacity(volumes)
	return s.Progress.saveUnlocked(p)
}

// ReviseOutline từ fromChapter thay thế đoạn đuôi kế hoạch chưa diễn ra. Dàn ý phẳng thay toàn bộ
// đoạn đuôi sách; dàn ý phân tầng chỉ thay đoạn đuôi của cung chứa chương đích. Định nghĩa này khiến
// cùng một payload replay vẫn ra cùng kết quả, đồng thời tránh phải liệt kê các thao tác JSON Patch
// và insert/delete.
func (s *Store) ReviseOutline(fromChapter int, replacement []domain.OutlineEntry) (int, error) {
	if fromChapter <= 0 {
		return 0, fmt.Errorf("from_chapter must be > 0: %w", errs.ErrToolArgs)
	}

	s.crossMu.Lock()
	defer s.crossMu.Unlock()

	s.Outline.io.mu.Lock()
	defer s.Outline.io.mu.Unlock()
	s.Progress.io.mu.Lock()
	defer s.Progress.io.mu.Unlock()

	p, err := s.Progress.loadUnlocked()
	if err != nil {
		return 0, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	if p == nil {
		return 0, fmt.Errorf("progress chưa khởi tạo: %w", errs.ErrToolPrecondition)
	}
	if p.Phase == domain.PhaseComplete {
		return 0, fmt.Errorf("toàn sách đã hoàn thành, không cho phép sửa dàn ý: %w", errs.ErrToolPrecondition)
	}
	protected := p.InProgressChapter
	if latest := p.LatestCompleted(); latest > protected {
		protected = latest
	}
	if fromChapter <= protected {
		// Chỉ báo "không được sửa" sẽ dồn bên gọi vào ngõ cụt: thực tế kiến trúc sư tại đây thử liền 4 lần
		// (from=21/22/13, rồi lùi về save_foundation(outline)) đều bị từ chối, xoay vòng đến khi đứt mạch.
		// Lỗi phải đồng thời nói rõ ai chịu trách nhiệm viết lại, nếu không kiến trúc sư sẽ tiếp tục tìm
		// trong bộ công cụ của mình một lối ra không hề tồn tại.
		return 0, fmt.Errorf(
			"Chương %d đã hoàn thành hoặc đang được viết; revise_outline chỉ có thể sửa các chương chưa diễn ra, phải bắt đầu sau chương %d."+
				"Các chương đã viết nằm trong pending_rewrites không thuộc diện sửa dàn ý: việc viết lại do writer thực hiện theo hàng đợi;"+
				"nếu kiến trúc sư không còn chương tương lai cần viết lại, hãy gọi resolve_outline_feedback xác nhận kế hoạch hiện tại vẫn phù hợp rồi kết thúc: %w",
			fromChapter, protected, errs.ErrToolPrecondition)
	}

	if p.Layered {
		volumes, err := s.Outline.reviseLayeredTailUnlocked(fromChapter, replacement)
		if err != nil {
			return 0, err
		}
		p.TotalChapters = domain.EstimatedChapterCapacity(volumes)
		if err := s.Progress.saveUnlocked(p); err != nil {
			return 0, fmt.Errorf("save progress: %w: %w", errs.ErrStoreWrite, err)
		}
		return p.TotalChapters, nil
	}

	outline, err := s.Outline.reviseFlatTailUnlocked(fromChapter, replacement)
	if err != nil {
		return 0, err
	}
	p.TotalChapters = len(outline)
	if err := s.Progress.saveUnlocked(p); err != nil {
		return 0, fmt.Errorf("save progress: %w: %w", errs.ErrStoreWrite, err)
	}
	return p.TotalChapters, nil
}

// ClearHandledSteer xóa PendingSteer và reset trạng thái FlowSteering phiên bản cũ. Hai tệp không
// thể tạo thành giao dịch hệ thống tệp, vì vậy ghi Progress có thể lặp lại trước, cuối cùng mới xóa ý
// định khôi phục; dù bước nào thất bại cũng ít nhất giữ lại PendingSteer, lần Resume sau có thể replay an toàn.
func (s *Store) ClearHandledSteer() error {
	s.crossMu.Lock()
	defer s.crossMu.Unlock()

	s.RunMeta.io.mu.Lock()
	defer s.RunMeta.io.mu.Unlock()
	s.Progress.io.mu.Lock()
	defer s.Progress.io.mu.Unlock()

	meta, err := s.RunMeta.loadUnlocked()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	p, err := s.Progress.loadUnlocked()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if p != nil && p.Flow == domain.FlowSteering {
		if err := domain.ValidateFlowTransition(p.Flow, domain.FlowWriting); err != nil {
			return err
		}
		p.Flow = domain.FlowWriting
		if err := s.Progress.saveUnlocked(p); err != nil {
			return err
		}
	}
	if meta != nil && meta.PendingSteer != "" {
		meta.PendingSteer = ""
		if err := s.RunMeta.saveUnlocked(*meta); err != nil {
			return err
		}
	}
	return nil
}
