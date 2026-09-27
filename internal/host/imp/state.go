package imp

import (
	"fmt"
	"os"

	"github.com/voocel/ainovel-cli/internal/store"
)

// Action là hành động tất định kế tiếp mà NextAction suy ra từ sự thật workspace.
// Trạng thái bền không ghi enum giai đoạn dễ trôi; hành động kế chỉ suy từ artifact (RFC §6.2).
type Action string

const (
	ActionIngest               Action = "ingest"
	ActionSegment              Action = "segment"
	ActionAwaitConfirmation    Action = "await_confirmation"
	ActionAnalyze              Action = "analyze"
	ActionSynthesize           Action = "synthesize"
	ActionAwaitStoryResolution Action = "await_story_resolution"
	ActionPublish              Action = "publish"
	ActionDone                 Action = "done"
)

// Facts là snapshot sự thật tối thiểu đọc từ workspace, đủ để quyết định hành động kế.
// Tách quyết định thuần (NextAction) khỏi IO (LoadState): NextAction hằng số với cùng một Facts (RFC §20.1).
type Facts struct {
	WorkspaceReady   bool // đủ bộ ba manifest + intent + source
	Segmented        bool
	Confirmed        bool
	ExpectedChapters int // tổng số chương đã xác nhận của phân tách (điền từ giai đoạn 2)
	AnalyzedChapters int // số phân tích liên tiếp từ chương 1 mà InputDigest khớp (điền từ giai đoạn 3)
	Synthesized      bool
	StoryUncertain   bool
	StoryResolved    bool
	Published        bool // artifact chính thức hoàn toàn khớp synthesis (điền từ giai đoạn 5)
}

// NextAction đi dọc pipeline tuyến tính cố định, trả về hành động đầu tiên còn thiếu hoặc chưa
// thỏa. Hàm thuần, không IO.
func NextAction(f Facts) Action {
	switch {
	case f.Published:
		// Công bố là trạng thái cuối: đối soát kho chính thức đã khớp toàn bộ, workspace chỉ còn
		// là hồ sơ kiểm toán. Artifact thượng nguồn hết tươi do nâng cấp phiên bản prompt / hướng
		// dẫn thì không yêu cầu làm lại — nếu không, nâng cấp phiên bản sẽ đẩy ngược sách đã công
		// bố về nửa đường, cổng chặn cross-restart của Engine khóa vĩnh viễn.
		return ActionDone
	case !f.WorkspaceReady:
		return ActionIngest
	case !f.Segmented:
		return ActionSegment
	case !f.Confirmed:
		return ActionAwaitConfirmation
	case f.AnalyzedChapters < f.ExpectedChapters:
		return ActionAnalyze
	case !f.Synthesized:
		return ActionSynthesize
	case f.StoryUncertain && !f.StoryResolved:
		return ActionAwaitStoryResolution
	default:
		return ActionPublish
	}
}

// artifactFresh kiểm tra artifact tồn tại và InputDigest của nó bằng want cần dựng lại hiện
// tại; thiếu, lỗi phân tích, schema hay digest mất khớp đều coi là hết tươi (cần làm lại).
func artifactFresh[T any](w *Workspace, rel, want string) (bool, error) {
	a, err := readArtifact[T](w, rel)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return a.InputDigest == want, nil
}

// LoadState đọc snapshot sự thật hiện tại từ workspace (chỉ workspace, không gồm Store chính thức).
// Ngắn mạch tuyến tính: mỗi bước đều kiểm tra InputDigest của artifact khớp với tóm tắt có thể
// dựng lại từ thượng nguồn hiện tại, một bước mất khớp coi là bước đó chưa xong, sự thật hạ nguồn
// giữ false, giao NextAction làm lại từ đó — chính điều này khiến "đổi phân tách / phiên bản prompt /
// nguồn" tự nhiên vô hiệu hạ nguồn (RFC §6.2/§6.3 / bất biến 1). Published do bên gọi bổ sung theo
// đối soát công bố chính thức (đi tập trung qua CollectFacts).
func LoadState(w *Workspace) (Facts, error) {
	var f Facts
	if !w.Active() {
		return f, nil
	}
	if !(w.has(fileManifest) && w.has(fileIntent) && w.has(fileSource)) {
		return f, nil
	}
	src, err := w.LoadSource()
	if err != nil {
		return f, fmt.Errorf("đọc snapshot nguồn nhập: %w", err)
	}
	f.WorkspaceReady = true
	guidance, err := w.LoadGuidance()
	if err != nil {
		return f, fmt.Errorf("đọc hướng dẫn phân tách: %w", err)
	}

	// segmentation: ràng buộc nguồn đã chuẩn hóa + hướng dẫn người dùng + phiên bản prompt phân tách.
	// Hướng dẫn thay đổi (--guide nhận diện lại) tự nhiên vô hiệu phân tách cũ.
	segArt, err := readArtifact[Segmentation](w, fileSegmentation)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("đọc artifact phân tách: %w", err)
	}
	if segArt.InputDigest != segmentInputDigest(Digest(src), guidance, segmentPromptVersion) {
		return f, nil
	}
	f.Segmented = true
	seg := &segArt.Payload
	f.ExpectedChapters = len(seg.Chapters)

	// confirmation: ràng buộc bytes gốc của artifact segmentation.
	segRaw, err := w.readBytes(fileSegmentation)
	if err != nil {
		return f, fmt.Errorf("đọc bytes gốc artifact phân tách: %w", err)
	}
	confirmed, err := artifactFresh[Confirmation](w, fileConfirmation, Digest(segRaw))
	if err != nil {
		return f, fmt.Errorf("đọc xác nhận phân tách: %w", err)
	}
	if !confirmed {
		return f, nil
	}
	f.Confirmed = true

	// Phân tích từng chương: số liên tiếp mà InputDigest từng chương khớp danh tính phân tách / phiên bản / chính văn.
	f.AnalyzedChapters, err = analyzedChaptersStrict(w, seg, src, segArt.InputDigest, analyzePromptVersion)
	if err != nil {
		return f, err
	}
	if f.AnalyzedChapters < f.ExpectedChapters {
		return f, nil
	}

	// synthesis: ràng buộc tập sự thật từng chương có thứ tự.
	facts, err := loadPriorFactsStrict(w, f.ExpectedChapters)
	if err != nil {
		return f, err
	}
	synArt, err := readArtifact[BookSynthesis](w, fileSynthesis)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("đọc artifact tổng hợp toàn sách: %w", err)
	}
	if synArt.InputDigest != synthesisInputDigest(facts) {
		return f, nil
	}
	f.Synthesized = true
	f.StoryUncertain = synArt.Payload.StoryStatus == storyUncertain

	// story resolution: khi uncertain thì ràng buộc bytes gốc artifact synthesis, hoặc do intent tiền chọn.
	synRaw, err := w.readBytes(fileSynthesis)
	if err != nil {
		return f, fmt.Errorf("đọc bytes gốc artifact tổng hợp toàn sách: %w", err)
	}
	resolved, err := artifactFresh[StoryResolution](w, fileStoryResolve, Digest(synRaw))
	if err != nil {
		return f, fmt.Errorf("đọc phán định trạng thái truyện: %w", err)
	}
	if resolved {
		f.StoryResolved = true
	} else if in, iErr := w.LoadIntent(); iErr != nil {
		return f, fmt.Errorf("đọc ý định nhập: %w", iErr)
	} else if in.StoryResolution != "" {
		f.StoryResolved = true
	}
	return f, nil
}

// CollectFacts kết hợp sự thật workspace với đối soát công bố chính thức, là cổng sự thật thống
// nhất cho ResumeStatus/ResumeSummary/runner. Số chương kỳ vọng của đối soát công bố ưu tiên lấy
// phân tách còn tươi; khi phân tách mất khớp do nâng cấp phiên bản prompt / hướng dẫn thì lùi về
// số chương đã xác nhận trong artifact — chương chính thức của sách đã công bố chính là nộp theo
// phân tách đó, dùng phiên bản hiện tại tính lại digest để đối soát thì lại không khớp cái gì cả.
func CollectFacts(st *store.Store, w *Workspace) (Facts, error) {
	f, err := LoadState(w)
	if err != nil {
		return f, err
	}
	expected := f.ExpectedChapters
	if expected == 0 {
		if segArt, err := readArtifact[Segmentation](w, fileSegmentation); err == nil {
			expected = len(segArt.Payload.Chapters)
		}
	}
	f.Published, err = isPublished(st, expected)
	return f, err
}

// ResumeStatus báo cáo có workspace nhập đang hoạt động hay không, và nó đã hoàn tất trọn vẹn
// chưa (gồm đối soát công bố chính thức). Dùng cho cổng chặn Engine cross-restart (RFC §12.5):
// khi active && !done thì cấm luồng sáng tác thường tiêu thụ trạng thái nửa công bố.
func ResumeStatus(st *store.Store) (active, done bool, err error) {
	w := OpenWorkspace(st.Dir())
	if !w.Active() {
		return false, false, nil
	}
	f, err := CollectFacts(st, w)
	if err != nil {
		return true, false, err
	}
	return true, NextAction(f) == ActionDone, nil
}

// ResumeSummary sinh một dòng nhắc cho lượt nhập chưa hoàn thành (RFC §18.2); không có lượt
// nhập dở thì trả chuỗi rỗng. Để host chủ động báo trên giao diện khởi động/chào mừng, tránh người
// dùng chỉ phát hiện cuốn sách kẹt nửa đường nhập khi sáng tác bị cổng chặn từ chối.
func ResumeSummary(st *store.Store) string {
	w := OpenWorkspace(st.Dir())
	if !w.Active() {
		return ""
	}
	f, err := CollectFacts(st, w)
	if err != nil {
		return "Phát hiện lỗi đọc trạng thái nhập: " + err.Error() + "; hãy chạy /import để xem và sửa"
	}
	var state string
	switch NextAction(f) {
	case ActionDone:
		return ""
	case ActionIngest, ActionSegment:
		state = "chưa hoàn thành phân tách"
	case ActionAwaitConfirmation:
		state = fmt.Sprintf("đã phân tách %d chương, chờ kiểm tra xác nhận", f.ExpectedChapters)
	case ActionAnalyze:
		state = fmt.Sprintf("đã phân tích %d/%d chương", f.AnalyzedChapters, f.ExpectedChapters)
	case ActionSynthesize:
		state = "phân tích từng chương xong, chờ tổng hợp toàn sách"
	case ActionAwaitStoryResolution:
		state = "chờ làm rõ trạng thái truyện (--story=open|closed)"
	case ActionPublish:
		state = "tổng hợp xong, chờ công bố trạng thái chính thức"
	}
	return "Phát hiện lượt nhập chưa hoàn thành (" + state + "), gõ /import để khôi phục từ checkpoint"
}

// checkImportPreconditions kiểm tra điều kiệntrước của lượt nhập mới (RFC §12.1): không có thông
// tin tác phẩm sẵn có, chương đã hoàn thành và PendingCommit đang trên đường. Ngữ nghĩa gộp tiểu
// thuyết sẵn có với văn bản ngoài mới không rõ ràng, bản đầu tiên từ chối thẳng.
func checkImportPreconditions(st *store.Store) error {
	book, err := st.Book.Load()
	if err != nil {
		return fmt.Errorf("đọc thông tin tác phẩm: %w", err)
	}
	if book != nil {
		return fmt.Errorf("đã có tác phẩm 《%s》, từ chối nhập tiểu thuyết ngoài vào sách không trống", book.Title)
	}
	prog, err := st.Progress.Load()
	if err != nil {
		return fmt.Errorf("đọc tiến độ: %w", err)
	}
	if prog != nil && len(prog.CompletedChapters) > 0 {
		return fmt.Errorf("đã có %d chương hoàn thành, từ chối nhập tiểu thuyết ngoài vào sách không trống", len(prog.CompletedChapters))
	}
	pending, err := st.Signals.LoadPendingCommit()
	if err != nil {
		return fmt.Errorf("đọc lệnh nộp đang trên đường: %w", err)
	}
	if pending != nil {
		return fmt.Errorf("tồn tại lệnh nộp chương đang trên đường, hãy hoàn tất hoặc dọn dẹp trước khi nhập")
	}
	return nil
}
