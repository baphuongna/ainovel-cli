package imp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/logger"
	"github.com/voocel/ainovel-cli/internal/store"
)

// Phiên bản prompt/schema đưa vào InputDigest của từng giai đoạn; nâng cấp hợp đồng prompt thì
// tăng để hạ nguồn artifact tự nhiên vô hiệu.
const (
	segmentPromptVersion = "seg-v2" // v2: ranh giới chỉ rơi vào điểm phân tách thật, tiêu đề sao chép nguyên chữ (phối hợp kiểm tra hiển thị lại tiêu đề)
	analyzePromptVersion = "analyze-v1"
	confirmMethodAuto    = "auto_authorized"
	confirmMethodUser    = "user_confirmed" // xác nhận thủ công rõ ràng bằng y sau khi TUI xem trước
)

// Prompts là system prompt của từng hàm ngữ nghĩa. Tổng hợp chia hai giai đoạn: Synthesize ra
// BookSynthesis toàn sách, Range ra RangeDigest khoảng liên tiếp cho truyện dài; hai cái có cấu
// trúc đầu ra khác nhau, phải dùng đúng prompt tương ứng.
type Prompts struct {
	Segment    string
	Analyze    string
	Synthesize string
	Range      string
}

// RunBudgets là ngân sách đầu vào/đầu ra của từng hàm ngữ nghĩa. Bản đầu dùng hằng số bảo thủ;
// tương lai nên suy từ context window / trần completion của mô hình architect hiện hành, để lô tự
// phình theo khả năng (RFC §9.2/§21).
type RunBudgets struct {
	MaxUnitBytes         int
	SegmentChunkBytes    int
	SegmentContextMargin int
	SegmentMaxTokens     int
	Analyze              AnalyzeBudget
	SynthesizeRangeBytes int
	SynthesizeMaxTokens  int
}

// DefaultRunBudgets trả ngân sách mặc định bảo thủ, dùng làm fallback khi khả năng mô hình không biết (dò thất bại).
func DefaultRunBudgets() RunBudgets {
	return RunBudgets{
		MaxUnitBytes:         8000,
		SegmentChunkBytes:    24000,
		SegmentContextMargin: 20,
		SegmentMaxTokens:     8192,
		Analyze:              AnalyzeBudget{ContextBytes: 24000, MaxOutputTokens: 8000, PerChapterOutput: 900, PromptOverhead: 2000},
		SynthesizeRangeBytes: 16000,
		SynthesizeMaxTokens:  8192,
	}
}

// ModelRuntime mang sự thật khả năng mô hình cần cho lời gọi ngữ nghĩa của imp, do Host inject sau
// khi dò biên (RFC §13/§17). Cho hai ngân sách tự phình theo context/completion, thinking gửi theo
// khả năng; toàn giá trị 0 thì lùi về mặc định bảo thủ, hành vi giống trước khi nối capability. Đầu ra
// có cấu trúc không gửi response_format theo khả năng provider (xem ghi chú ở callProfile).
type ModelRuntime struct {
	ContextTokens   int                     // trần ngữ cảnh đầu vào (token)
	MaxOutputTokens int                     // trần đầu ra khả kiến mỗi lần (token)
	Thinking        agentcore.ThinkingLevel // đã resolve theo khả năng; ThinkingAuto("") nghĩa là không gửi tường minh
}

// profile suy ra tùy chọn khả năng gọi (thinking) của runtime này.
func (rt ModelRuntime) profile() callProfile {
	return callProfile{thinking: rt.Thinking}
}

// Caller là cấp mô hình của một hàm ngữ nghĩa: mô hình + sự thật khả năng của mô hình đó
// (RFC §13.1/§17). segment/analyze/synthesize mỗi cái giữ cấp riêng, ngân sách và tùy chọn gọi đều
// suy theo cấp của mình, cửa sổ nhỏ của cấp rẻ chỉ ràng buộc hàm của nó, không kéo lê giai đoạn khác.
type Caller struct {
	Model   callModel
	Runtime ModelRuntime
}

// budgetsFromRuntime suy ngân sách từng hàm ngữ nghĩa từ trần context/completion thật của mô hình
// (RFC §9.2/§21). Chính điều này khiến "đổi mô hình mạnh hơn tự phình lô, giảm số lời gọi" thành
// lập; khả năng không biết thì lùi về mặc định bảo thủ.
func budgetsFromRuntime(rt ModelRuntime) RunBudgets {
	if rt.ContextTokens <= 0 || rt.MaxOutputTokens <= 0 {
		return DefaultRunBudgets()
	}
	const bytesPerToken = 3 // quy đổi bảo thủ UTF-8 chữ Hán: token→byte (ước thấp dung lượng thì an toàn hơn)
	out := rt.MaxOutputTokens
	// Ngân sách đầu vào: ngữ cảnh trừ đầu ra khả kiến và ~10% dự phòng suy luận/hệ thống rồi quy ra byte.
	reserve := rt.ContextTokens / 10
	inTokens := rt.ContextTokens - out - reserve
	if inTokens < 2000 {
		inTokens = 2000
	}
	inBytes := inTokens * bytesPerToken
	return RunBudgets{
		MaxUnitBytes:         min(inBytes/2, 32000),
		SegmentChunkBytes:    inBytes,
		SegmentContextMargin: 20,
		SegmentMaxTokens:     out,
		Analyze: AnalyzeBudget{
			ContextBytes:     inBytes,
			MaxOutputTokens:  out,
			PerChapterOutput: 900,
			PromptOverhead:   2000,
		},
		SynthesizeRangeBytes: inBytes,
		SynthesizeMaxTokens:  out,
	}
}

// Confirmation là artifact xác nhận phân tách, ràng buộc segmentation hiện tại (RFC §8.4).
type Confirmation struct {
	Method   string `json:"method"`
	Chapters int    `json:"chapters"`
}

// StoryResolution là phán định của người dùng cho trạng thái truyện uncertain, ràng buộc synthesis hiện tại (RFC §10.4).
type StoryResolution struct {
	Choice string `json:"choice"` // open / closed
}

// Deps là dependency hẹp của runner (RFC §17). Ba hàm ngữ nghĩa mỗi cái khai báo cấp mô hình
// riêng; Host mặc định rơi hết vào architect, tầng cấu hình có thể trỏ hàm cơ khí hơn sang cấp rẻ hơn (RFC §13.1).
type Deps struct {
	Store         *store.Store
	CommitChapter ChapterCommitter
	Segment       Caller
	Analyze       Caller
	Synthesize    Caller // range digest và book synthesis cùng một cấp (cùng giai đoạn tổng hợp)
	Prompts       Prompts
	Budgets       RunBudgets
}

// budgetsFromDeps suy ngân sách theo khả năng cấp riêng của từng hàm ngữ nghĩa (RFC §9.2/§13.1).
func budgetsFromDeps(d Deps) RunBudgets {
	seg := budgetsFromRuntime(d.Segment.Runtime)
	ana := budgetsFromRuntime(d.Analyze.Runtime)
	syn := budgetsFromRuntime(d.Synthesize.Runtime)
	return RunBudgets{
		MaxUnitBytes:         seg.MaxUnitBytes,
		SegmentChunkBytes:    seg.SegmentChunkBytes,
		SegmentContextMargin: seg.SegmentContextMargin,
		SegmentMaxTokens:     seg.SegmentMaxTokens,
		Analyze:              ana.Analyze,
		SynthesizeRangeBytes: syn.SynthesizeRangeBytes,
		SynthesizeMaxTokens:  syn.SynthesizeMaxTokens,
	}
}

// Run thi hành pipeline nhập trọn vẹn: LoadState → NextAction → thi hành một hành động → đọc lại
// sự thật. Chạy trong goroutine riêng; kênh sự kiện trả về do hàm này đóng.
func Run(ctx context.Context, deps Deps, opts Options) (<-chan Event, error) {
	if deps.Store == nil || deps.CommitChapter == nil ||
		deps.Segment.Model == nil || deps.Analyze.Model == nil || deps.Synthesize.Model == nil {
		return nil, fmt.Errorf("deps không trọn vẹn")
	}
	if deps.Budgets == (RunBudgets{}) {
		deps.Budgets = budgetsFromDeps(deps)
	}
	// Log luồng nhập tách thành file riêng: bản ghi trọn vẹn một lượt nhập (sự kiện, thử lại, chuỗi
	// lỗi đầy đủ) không trộn với log engine/TUI, khi điều tra chỉ cần nhìn một file này. Tạo thất bại
	// phải hiển thị lại — bảng sẽ dẫn người dùng xem logs/import.log, lùi về im lặng bằng với trỏ vào
	// một file không tồn tại (Debug-First).
	log, closeLog, logErr := logger.FileLogger(deps.Store.Dir(), "import.log")
	log.Info("imp runtime mô hình nhập",
		"segment_ctx", deps.Segment.Runtime.ContextTokens,
		"analyze_ctx", deps.Analyze.Runtime.ContextTokens,
		"synthesize_ctx", deps.Synthesize.Runtime.ContextTokens,
		"analyze_max_output", deps.Analyze.Runtime.MaxOutputTokens,
		"analyze_context_bytes", deps.Budgets.Analyze.ContextBytes)
	events := make(chan Event, 32)
	go func() {
		defer close(events)
		defer closeLog()
		r := &runner{ctx: ctx, deps: deps, opts: opts, events: events, ws: OpenWorkspace(deps.Store.Dir()), log: log}
		if logErr != nil {
			r.emit(StageIngesting, 0, 0, fmt.Sprintf("tạo tệp log nhập thất bại (%v), bản ghi lần này chuyển qua log mặc định", logErr), nil)
		}
		r.run(ctx)
	}()
	return events, nil
}

type runner struct {
	ctx    context.Context // nhận biết hủy để gửi sự kiện trạng thái cuối (không chặn pipeline khi bên tiêu thụ ngừng đọc)
	deps   Deps
	opts   Options
	events chan Event
	ws     *Workspace
	act    Action       // hành động đang thi hành, để artifact thất bại ghi chú giai đoạn
	log    *slog.Logger // log riêng của lượt nhập (logs/import.log); nil thì lùi về logger mặc định
}

func (r *runner) emit(stage Stage, current, total int, msg string, err error) {
	r.send(Event{Time: time.Now(), Stage: stage, Current: current, Total: total, Message: msg, Err: err})
}

func (r *runner) send(ev Event) {
	r.logEvent(ev)
	// Sự kiện trạng thái cuối và điểm dừng mang tín hiệu thành bại/hành động duy nhất (mất bản xem
	// trước xác nhận, nhắc --story thì người dùng không biết phải làm gì), phải gửi tin cậy; chỉ sự
	// kiện tiến độ giữa mới được vứt khi tồn đọng.
	if ev.Stage == StageError || ev.Stage == StageDone ||
		ev.Stage == StageAwaitingConfirmation || ev.Stage == StageAwaitingStoryStatus {
		// Gửi tin cậy nhưng không chặn vô hạn: khi bên tiêu thụ ngừng đọc vì người dùng hủy, ưu tiên
		// để pipeline kết thúc theo ctx, nếu không goroutine sẽ kẹt vĩnh viễn ở đây. Bản ghi trọn vẹn
		// đã ghi xuống đĩa trước ở logEvent, tín hiệu trạng thái cuối không mất manh mối điều tra.
		// (r.ctx chỉ có thể rỗng trong test tự dựng runner trực tiếp, lùi về gửi chặn.)
		if r.ctx == nil {
			r.events <- ev
			return
		}
		select {
		case r.events <- ev:
		case <-r.ctx.Done():
		}
		return
	}
	select {
	case r.events <- ev:
	default: // kênh đầy thì vứt tiến độ, tuyệt đối không chặn pipeline
	}
}

// logEvent ghi chép từng sự kiện tiến độ vào log riêng của lượt nhập (<gốc sách>/logs/import.log):
// dòng thử lại trên bảng bị ghi đè tại chỗ, bảng biến mất theo Esc, log là bản ghi luồng đầy đủ duy
// nhất có thể điều tra sau (§14.1).
func (r *runner) logEvent(ev Event) {
	log := r.log
	if log == nil {
		log = slog.Default()
	}
	args := []any{"stage", string(ev.Stage)}
	if ev.Total > 0 {
		args = append(args, "progress", fmt.Sprintf("%d/%d", ev.Current, ev.Total))
	}
	if ev.Err != nil {
		args = append(args, "err", ev.Err)
	}
	level := slog.LevelInfo
	switch {
	case ev.Stage == StageError:
		level = slog.LevelError // trạng thái cuối thất bại là dòng đáng bị lọc ra nhất trong log, không được rơi thành INFO
	case ev.Level == "warn":
		level = slog.LevelWarn
	}
	log.Log(context.Background(), level, ev.Message, args...)
}

func (r *runner) fail(msg string, err error) {
	r.saveFailure(err)
	r.emit(StageError, 0, 0, msg, err)
}

// saveFailure tập trung ghi các thất bại mang phản hồi gốc vào failures/ (điểm rơi thứ ba của
// RFC §14.2), mọi hàm ngữ nghĩa như segment/synthesize dùng chung fallback này; đường cứu vớt tiền tố
// khi phân tích bị cắt đã ghi metadata tinh hơn tại chỗ. Thất bại không có phản hồi gốc (IO, hủy,
// kiểm tra trước) không có đầu ra mô hình để lưu, không ghi.
func (r *runner) saveFailure(err error) {
	var se *errSemantic
	var tr *errTruncated
	switch {
	case errors.As(err, &se):
		r.ws.writeFailure(FailureMeta{Stage: string(r.act), Detail: err.Error()}, se.Raw)
	case errors.As(err, &tr):
		r.ws.writeFailure(FailureMeta{Stage: string(r.act), Detail: err.Error(), StopReason: "length"}, tr.Raw)
	}
}

// facts kết hợp sự thật workspace với đối soát công bố chính thức.
func (r *runner) facts() (Facts, error) {
	return CollectFacts(r.deps.Store, r.ws)
}

// profileFor suy tùy chọn gọi của một cấp, và hiển thị lại backoff yêu cầu / hỏi lại sau kiểm tra
// vào dòng sự kiện của giai đoạn tương ứng — backoff thử lại có thể im lặng luỹ kế hơn 2 phút, không
// hiển thị lại thì người dùng tưởng treo (§14.1). Key chỉ cho backoff yêu cầu (kèm mốc hết hạn): nó
// là trạng thái nhất thời trong cùng một lời gọi, UI cập nhật tại chỗ một dòng (số "lần thứ N" nhấp
// nháy). Hỏi lại sau kiểm tra là sự kiện ngữ nghĩa xuyên lời gọi — phân tách gọi từng khối, mỗi khối
// hỏi lại độc lập, dùng chung Key sẽ khiến khối sau đè khối trước, nuốt mất manh mối điều tra (thực đo bảng
// chỉ còn một dòng unit_id không ngừng đổi), vì vậy mỗi cái một dòng giữ lại lịch sử.
func (r *runner) profileFor(c Caller, stage Stage) callProfile {
	prof := c.Runtime.profile()
	prof.log = r.log
	prof.notify = func(msg string, retryAt time.Time) {
		ev := Event{Time: time.Now(), Stage: stage, Message: msg, Level: "warn", RetryAt: retryAt}
		if !retryAt.IsZero() {
			ev.Key = "retry:" + string(stage)
		}
		r.send(ev)
	}
	prof.progress = func(current, total int, msg string) {
		r.send(Event{Time: time.Now(), Stage: stage, Current: current, Total: total, Message: msg})
	}
	return prof
}

// applyGuidance bền hóa hướng dẫn tường minh --guide lần này thành đầu vào ngữ nghĩa của workspace
// (RFC §18.3). Hướng dẫn là một đầu vào của segmentation InputDigest: nội dung thay đổi tự nhiên làm
// phân tách cũ và toàn bộ hạ nguồn mất khớp rồi làm lại, không viết quy tắc vô hiệu thủ công. Khi
// workspace chưa dựng thì tạm bỏ qua, vòng lặp kế sau ingest sẽ ghi.
func (r *runner) applyGuidance() error {
	g := strings.TrimSpace(r.opts.Guidance)
	if g == "" || !r.ws.Active() {
		return nil
	}
	existing, err := r.ws.LoadGuidance()
	if err != nil {
		return fmt.Errorf("đọc hướng dẫn phân tách sẵn có: %w", err)
	}
	if existing == g {
		return nil
	}
	// Sau khi công bố bắt đầu, artifact chính thức không thể ghi đè (§12.2): lúc này cắt lại chắc
	// chắn đụng "từ chối ghi đè" tường chết ở publish, và trước khi đụng tường sẽ trả lại tiền toàn
	// chuỗi lời gọi mô hình phân tách/phân tích/tổng hợp — đẩy thất bại lên điểm chi phí bằng 0. book
	// là khoản ghi đầu tiên của công bố, nó tồn tại nghĩa là công bố đã bắt đầu (kiểm tra trước của lượt
	// nhập bảo đảm sách vốn trống).
	book, err := r.deps.Store.Book.Load()
	if err != nil {
		return fmt.Errorf("đọc book chính thức: %w", err)
	}
	if book != nil {
		return fmt.Errorf("Foundation chính thức đã bắt đầu công bố, cắt lại bằng --guide sẽ xung đột nội dung đã công bố mà bị từ chối ghi đè, không còn nhận hướng dẫn phân tách")
	}
	return r.ws.writeAtomic(fileGuidance, []byte(g))
}

// checkSourceIdentity chặn "workspace đang tiến hành lại truyền tệp nguồn khác": ingest chỉ thi
// hành khi không có workspace, nếu không so sánh thì /import B.txt sẽ im lặng tiếp tục từ checkpoint
// của A, công bố xong A mà B không đọc được một byte nào (RFC §12.1/§18.2). Truyền lặp lại đường dẫn
// cùng một tệp là thói quen thường (khôi phục /import cùng đường dẫn), so theo tóm tắt nội dung thay
// vì từ chối mọi đường dẫn.
func (r *runner) checkSourceIdentity() error {
	if r.opts.SourcePath == "" || !r.ws.Active() {
		return nil
	}
	m, err := r.ws.LoadManifest()
	if err != nil {
		return nil // bộ ba danh tính không đọc được thì đi chẩn đoán hỏng của ingest, không lặp lại báo lỗi ở đây
	}
	raw, err := os.ReadFile(r.opts.SourcePath)
	if err != nil {
		return fmt.Errorf("đọc tệp nguồn %s: %w", r.opts.SourcePath, err)
	}
	if Digest(raw) != m.RawSHA256 {
		return fmt.Errorf("đã có lượt nhập %q đang tiến hành, tệp nguồn lần này có nội dung khác: hãy hoàn tất hoặc bỏ lượt nhập cũ (xóa meta/import/) rồi nhập sách mới", m.SourceName)
	}
	return nil
}

func (r *runner) run(ctx context.Context) {
	if err := r.checkSourceIdentity(); err != nil {
		r.fail("kiểm tra danh tính tệp nguồn", err)
		return
	}
	var previous *Facts
	for {
		if ctx.Err() != nil {
			r.fail("người dùng hủy", ctx.Err())
			return
		}
		if err := r.applyGuidance(); err != nil {
			r.fail("ghi hướng dẫn phân tách", err)
			return
		}
		facts, err := r.facts()
		if err != nil {
			r.fail("đọc trạng thái nhập", err)
			return
		}
		if previous != nil && facts == *previous {
			r.fail("lượt nhập trì trệ", fmt.Errorf("sự thật không đổi sau khi thi hành hành động, hành động kế vẫn là %q", NextAction(facts)))
			return
		}
		snapshot := facts
		previous = &snapshot
		act := NextAction(facts)
		r.act = act
		err = nil
		switch act {
		case ActionIngest:
			err = r.ingest(ctx)
		case ActionSegment:
			err = r.segment(ctx)
		case ActionAwaitConfirmation:
			if !r.confirm() {
				return // chế độ tương tác: chờ người dùng xác nhận, dừng tại đây
			}
		case ActionAnalyze:
			err = r.analyze(ctx)
		case ActionSynthesize:
			err = r.synthesize(ctx)
		case ActionAwaitStoryResolution:
			if !r.resolveStoryStatus() {
				return // không có phán định tường minh: dừng tại đây, chờ --story=open|closed
			}
		case ActionPublish:
			err = r.publish(ctx)
		case ActionDone:
			r.emit(StageDone, 0, 0, "nhập hoàn tất, chờ nghiệm thu rồi viết tiếp", nil)
			return
		default:
			err = fmt.Errorf("hành động lạ %q", act)
		}
		if err != nil {
			r.fail("nhập thất bại", err)
			return
		}
	}
}

func (r *runner) ingest(ctx context.Context) error {
	// Đến ingest mà thư mục đã tồn tại = bộ ba danh tính (manifest/source/intent) thiếu hoặc hỏng:
	// createWorkspace sẽ từ chối với "đã tồn tại (/import không tham số để khôi phục)", chạy lại
	// không tham số lại quay về đây đòi đường dẫn nguồn vì WorkspaceReady=false — hai lời nhắc đánh
	// nhau, người dùng không còn đường nào.
	if r.ws.Active() {
		return fmt.Errorf("meta/import/ đã tồn tại nhưng danh tính workspace không dùng được (manifest/source/intent thiếu hoặc hỏng), hãy xác nhận thủ công rồi xóa thư mục này và nhập lại")
	}
	if err := checkImportPreconditions(r.deps.Store); err != nil {
		return err
	}
	if r.opts.SourcePath == "" {
		return fmt.Errorf("lượt nhập mới cần đường dẫn tệp nguồn")
	}
	r.emit(StageIngesting, 0, 0, "đọc, giải mã, chuẩn hóa và chụp snapshot tệp nguồn...", nil)
	_, m, err := Ingest(r.deps.Store.Dir(), r.opts.SourcePath, r.opts.intent())
	if err != nil {
		return err
	}
	r.emit(StageIngesting, 0, 0, fmt.Sprintf("snapshot nguồn sẵn sàng: %s (mã hóa %s, %d byte)", m.SourceName, m.Encoding, m.SizeBytes), nil)
	return nil
}

func (r *runner) segment(ctx context.Context) error {
	src, err := r.ws.LoadSource()
	if err != nil {
		return err
	}
	units := buildSourceUnits(src, r.deps.Budgets.MaxUnitBytes)
	guidance, err := r.ws.LoadGuidance()
	if err != nil {
		return fmt.Errorf("đọc hướng dẫn phân tách: %w", err)
	}
	r.emit(StageSegmenting, 0, 0, fmt.Sprintf("nhận diện ngữ nghĩa ranh giới chương (%d unit tọa độ)...", len(units)), nil)
	digest := segmentInputDigest(Digest(src), guidance, segmentPromptVersion)
	// Danh tính cache khối ràng thêm MaxUnitBytes: bảng unit do (nguồn chuẩn hóa, MaxUnitBytes)
	// quyết định duy nhất, đổi cấp mô hình làm đổi MaxUnitBytes sẽ nắn lại phân mảnh ảo của dòng siêu
	// dài — chuỗi ID (L1.1…) và điểm cuối khối tái lập được nhưng phạm vi byte đã đổi, chỉ khớp theo
	// ID điểm cuối sẽ tái dùng ranh giới cũ lệch chỗ (anchor mất khớp thất bại tất định hoặc cắt sai
	// vô thanh).
	chunkIdentity := fmt.Sprintf("%s\x00units:%d", digest, r.deps.Budgets.MaxUnitBytes)
	seg, err := Segment(ctx, r.deps.Segment.Model, r.deps.Prompts.Segment, src, units, guidance,
		r.deps.Budgets.SegmentChunkBytes, r.deps.Budgets.SegmentContextMargin, r.deps.Budgets.SegmentMaxTokens,
		r.profileFor(r.deps.Segment, StageSegmenting), r.ws, chunkIdentity)
	if err != nil {
		return err
	}
	if err := writeArtifact(r.ws, fileSegmentation, digest, *seg); err != nil {
		return err
	}
	// Phân tách cuối đã ghi xuống đĩa, cache cấp khối hoàn thành nhiệm vụ; xóa thất bại không hại
	// tính đúng đắn (digest vẫn nhất quán), nhưng phải để dấu vết.
	if cerr := r.ws.clearDir(dirSegmentChunks); cerr != nil {
		r.emit(StageSegmenting, 0, 0, fmt.Sprintf("dọn cache cấp khối thất bại (không ảnh hưởng kết quả phân tách): %v", cerr), nil)
	}
	r.emit(StageSegmenting, len(seg.Chapters), len(seg.Chapters),
		fmt.Sprintf("phân tách hoàn tất: %d chương, %d khu vực phụ trợ", len(seg.Chapters), len(seg.Matter)), nil)
	return nil
}

// confirm xử lý xác nhận phân tách. --yes tự chấp nhận và ghi artifact confirmation; ngược lại trình bày bản xem trước rồi dừng.
func (r *runner) confirm() bool {
	seg, err := readArtifact[Segmentation](r.ws, fileSegmentation)
	if err != nil {
		r.fail("đọc kết quả phân tách", err)
		return false
	}
	in, err := r.ws.LoadIntent()
	if err != nil {
		r.fail("đọc ý định nhập", err)
		return false
	}
	accept := r.opts.AcceptSegmentation
	auto := r.opts.AutoConfirm || (in != nil && in.AutoConfirm)
	// Phân tách từng xảy ra dung sai ngữ nghĩa (Notes khác rỗng: hấp thụ chương rỗng/chặn đầu/khử
	// trùng vị trí trùng) thì không do --yes phê duyệt mù: cấu trúc đã bị sửa lại tất định, bắt buộc
	// kiểm tra thủ công — nếu không ghi chú dung sai dưới --yes không ai thấy, bằng sửa vô thanh.
	// Nhấn y sau khi TUI xem trước thì đi AcceptSegmentation (phán định tường minh sau khi đã xem),
	// không chịu giới hạn này.
	blockedByNotes := auto && !accept && len(seg.Payload.Notes) > 0
	if blockedByNotes {
		auto = false
	}
	if !auto && !accept {
		msg := buildConfirmPreview(&seg.Payload)
		if blockedByNotes {
			msg += "  ! Có ghi chú dung sai phân tách, --yes không tự động phê duyệt, hãy kiểm tra thủ công\n"
		}
		r.emit(StageAwaitingConfirmation, len(seg.Payload.Chapters), len(seg.Payload.Chapters), msg, nil)
		return false
	}
	raw, err := r.ws.readBytes(fileSegmentation)
	if err != nil {
		r.fail("đọc artifact phân tách", err)
		return false
	}
	method, doneMsg := confirmMethodAuto, "đã tự động chấp nhận phân tách (--yes)"
	if accept {
		method, doneMsg = confirmMethodUser, "đã xác nhận phân tách (kiểm tra thủ công)"
	}
	conf := Confirmation{Method: method, Chapters: len(seg.Payload.Chapters)}
	if err := writeArtifact(r.ws, fileConfirmation, Digest(raw), conf); err != nil {
		r.fail("ghi artifact xác nhận", err)
		return false
	}
	r.emit(StageAwaitingConfirmation, len(seg.Payload.Chapters), len(seg.Payload.Chapters), doneMsg, nil)
	return true
}

// buildConfirmPreview lắp bản xem trước xác nhận phân tách: số chương, khu vực phụ trợ, toàn bộ tiêu
// đề chương và dấu uncertain (RFC §8.4). Liệt kê toàn bộ, viewport của bảng cuộn xem được; không đặt
// trần cắt.
func buildConfirmPreview(seg *Segmentation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Đã phân tách %d chương", len(seg.Chapters))
	if len(seg.Matter) > 0 {
		fmt.Fprintf(&b, ", %d khu vực phụ trợ", len(seg.Matter))
	}
	if len(seg.Uncertain) > 0 {
		fmt.Fprintf(&b, " (%d chương còn nghi vấn)", len(seg.Uncertain))
	}
	b.WriteString(", vui lòng kiểm tra:\n")
	uncertain := make(map[int]bool, len(seg.Uncertain))
	for _, n := range seg.Uncertain {
		uncertain[n] = true
	}
	for _, c := range seg.Chapters {
		fmt.Fprintf(&b, "  Chương %d %s", c.Number, c.Title)
		if uncertain[c.Number] {
			b.WriteString("  [còn nghi vấn]")
		}
		b.WriteByte('\n')
	}
	for _, mt := range seg.Matter {
		fmt.Fprintf(&b, "  [%s] %s\n", mt.Kind, mt.Title)
	}
	// Ghi chú dung sai giai đoạn phân tách (như tiêu đề placeholder không chính văn nhập vào đoạn
	// trước) bắt buộc trình bày tại điểm dừng thủ công, nếu không hành vi hấp thụ biến thành sửa vô thanh.
	for _, n := range seg.Notes {
		fmt.Fprintf(&b, "  ! %s\n", n)
	}
	// Nhắc thao tác (y xác nhận / --guide cắt lại / Esc) do khối tạm dừng của TUI render thống nhất,
	// chỗ này chỉ giữ sự thật, tránh hai bản văn bản trôi lệch nhau.
	return b.String()
}

func (r *runner) analyze(ctx context.Context) error {
	src, err := r.ws.LoadSource()
	if err != nil {
		return err
	}
	segArt, err := readArtifact[Segmentation](r.ws, fileSegmentation)
	if err != nil {
		return err
	}
	seg := &segArt.Payload
	total := len(seg.Chapters)
	// Digest từng chương chỉ ràng chính văn chương đó, không gồm ngữ cảnh lô và ledger trước đó. Nếu
	// chương K vì thiếu/mất khớp cần phân tích lại, đằng sau vẫn để artifact cũ digest tình cờ khớp sẽ
	// bị tái dùng cùng ledger đã vô hiệu. Trước khi mở phân tích, dọn đuôi vượt qua tiền tố tươi, cưỡng
	// "phân tích lại chương nào là vô hiệu toàn bộ phân tích phía sau", sau đó phân tích thuận chiều
	// không còn sinh đuôi cũ (RFC §9.6 / #4a).
	if err := discardAnalysesAfter(r.ws, analyzedChapters(r.ws, seg, src, segArt.InputDigest, analyzePromptVersion), total); err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		start := analyzedChapters(r.ws, seg, src, segArt.InputDigest, analyzePromptVersion)
		if start >= total {
			break
		}
		r.emit(StageAnalyzing, start, total, fmt.Sprintf("phân tích lô liên tiếp bắt đầu từ chương %d...", start+1), nil)
		done, err := AnalyzeNext(ctx, r.deps.Analyze.Model, r.deps.Prompts.Analyze, r.ws, src, seg, segArt.InputDigest, analyzePromptVersion, r.deps.Budgets.Analyze, r.profileFor(r.deps.Analyze, StageAnalyzing))
		if err != nil {
			return err
		}
		if done == 0 {
			break
		}
	}
	r.emit(StageAnalyzing, total, total, "trích xuất sự thực từng chương hoàn tất", nil)
	return nil
}

func (r *runner) synthesize(ctx context.Context) error {
	segArt, err := readArtifact[Segmentation](r.ws, fileSegmentation)
	if err != nil {
		return err
	}
	total := len(segArt.Payload.Chapters)
	facts := loadPriorFacts(r.ws, total)
	if len(facts) != total {
		return fmt.Errorf("phân tích từng chương chưa trọn: %d/%d", len(facts), total)
	}
	r.emit(StageSynthesizing, 0, total, "quy nạp phân tầng ngữ nghĩa toàn sách...", nil)
	syn, err := Synthesize(ctx, r.deps.Synthesize.Model, r.deps.Prompts.Synthesize, r.deps.Prompts.Range, r.ws, facts,
		r.deps.Budgets.SynthesizeRangeBytes, r.deps.Budgets.SynthesizeMaxTokens, r.profileFor(r.deps.Synthesize, StageSynthesizing))
	if err != nil {
		return err
	}
	if err := writeArtifact(r.ws, fileSynthesis, synthesisInputDigest(facts), *syn); err != nil {
		return err
	}
	r.emit(StageSynthesizing, total, total, fmt.Sprintf("tổng hợp hoàn tất: %d tập, trạng thái truyện %s", len(syn.Structure), syn.StoryStatus), nil)
	return nil
}

func (r *runner) publish(ctx context.Context) error {
	synArt, err := readArtifact[BookSynthesis](r.ws, fileSynthesis)
	if err != nil {
		return err
	}
	segArt, err := readArtifact[Segmentation](r.ws, fileSegmentation)
	if err != nil {
		return err
	}
	seg := &segArt.Payload
	src, err := r.ws.LoadSource()
	if err != nil {
		return err
	}
	total := len(seg.Chapters)
	facts := loadPriorFacts(r.ws, total)
	if len(facts) != total {
		return fmt.Errorf("phân tích chưa trọn trước khi công bố: %d/%d", len(facts), total)
	}
	closed, err := r.resolveStory(&synArt.Payload)
	if err != nil {
		return err
	}
	manifest, err := r.ws.LoadManifest()
	if err != nil {
		return err
	}
	f, err := AssembleFoundation(&synArt.Payload, facts, closed, manifest.SourceName)
	if err != nil {
		return err
	}
	r.emit(StageValidating, 0, total, "kiểm tra lắp ráp Foundation đạt", nil)

	r.emit(StagePublishing, 0, total, "công bố Foundation chính thức...", nil)
	if err := publishFoundation(r.deps.Store, f); err != nil {
		return err
	}
	// Hold hoàn tất nhập phải bền hóa sớm hơn bất kỳ khoản nộp chương nào: nếu sập vào giữa "nộp
	// chương cuối" và "đặt Hold", khởi động lại isPublished=true → lượt nhập bị coi là hoàn tất mà
	// thiếu Hold, Engine sẽ nhầm sách nhập là sách dừng máy thường mà viết tiếp. Đặt sau
	// publishFoundation (đã khởi tạo RunMeta), trước khi nộp chương, đóng kín cửa sổ này; chạy lại
	// công bố thì đặt lại idempotent (--continue không đặt Hold, giao cho tự động tiếp nối, RFC §12.4).
	if err := r.setCompletionHold(); err != nil {
		return fmt.Errorf("thiết lập Hold hoàn tất nhập: %w", err)
	}
	for i, c := range seg.Chapters {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.emit(StagePublishing, c.Number, total, fmt.Sprintf("công bố chương %d/%d: %s", c.Number, total, c.Title), nil)
		if err := publishChapter(ctx, r.deps.Store, r.deps.CommitChapter, c.Number, seg.Content(src, i), facts[i]); err != nil {
			return err
		}
	}
	return nil
}

// storyChoice trả phán định hữu hiệu cho trạng thái uncertain: ưu tiên phán định đã ghi ràng buộc
// synthesis hiện tại, kế đến opts lần này, sau cùng intent gốc. Phán định đã ghi bắt buộc kiểm tra
// InputDigest khớp synthesis hiện tại — tổng hợp lại thì phán định cũ vô hiệu, không thể im lặng
// khoán open/closed cũ lên kết quả mới, nếu không người dùng sẽ không được hỏi lại (RFC §10.4).
// --story tường minh (intent) là chỉ lệnh thường trú của người dùng xuyên tổng hợp, có thể giữ.
func (r *runner) storyChoice() (string, error) {
	if raw, err := r.ws.readBytes(fileSynthesis); err == nil {
		if art, aerr := readArtifact[StoryResolution](r.ws, fileStoryResolve); aerr == nil && art.InputDigest == Digest(raw) {
			return art.Payload.Choice, nil
		} else if aerr != nil && !os.IsNotExist(aerr) {
			return "", fmt.Errorf("đọc phán định trạng thái truyện: %w", aerr)
		}
	} else {
		return "", fmt.Errorf("đọc artifact tổng hợp: %w", err)
	}
	if r.opts.StoryResolution != "" {
		return r.opts.StoryResolution, nil
	}
	in, err := r.ws.LoadIntent()
	if err != nil {
		return "", fmt.Errorf("đọc ý định nhập: %w", err)
	}
	return in.StoryResolution, nil
}

// resolveStoryStatus khi uncertain mà đã có phán định tường minh thì ghi story-resolution.json (ràng
// synthesis hiện tại), để NextAction hạ nguồn tự nhiên phê duyệt; không phán định thì trình bày chờ và dừng.
func (r *runner) resolveStoryStatus() bool {
	choice, err := r.storyChoice()
	if err != nil {
		r.fail("đọc phán định trạng thái truyện", err)
		return false
	}
	if choice != storyOpen && choice != storyClosed {
		r.emit(StageAwaitingStoryStatus, 0, 0, "tổng hợp phán định trạng thái truyện là uncertain, hãy làm rõ bằng --story=open|closed rồi thử lại", nil)
		return false
	}
	raw, err := r.ws.readBytes(fileSynthesis)
	if err != nil {
		r.fail("đọc kết quả tổng hợp", err)
		return false
	}
	if err := writeArtifact(r.ws, fileStoryResolve, Digest(raw), StoryResolution{Choice: choice}); err != nil {
		r.fail("ghi phán định trạng thái truyện", err)
		return false
	}
	return true
}

// resolveStory dựa vào kết quả tổng hợp và phán định tường minh của người dùng đưa ra trạng thái
// khép của truyện (RFC §10.4).
func (r *runner) resolveStory(syn *BookSynthesis) (bool, error) {
	switch syn.StoryStatus {
	case storyClosed:
		return true, nil
	case storyOpen:
		return false, nil
	case storyUncertain:
		choice, err := r.storyChoice()
		if err != nil {
			return false, err
		}
		switch choice {
		case storyClosed:
			return true, nil
		case storyOpen:
			return false, nil
		default:
			return false, fmt.Errorf("trạng thái truyện uncertain, cần --story=open|closed")
		}
	default:
		return false, fmt.Errorf("story_status lạ: %q", syn.StoryStatus)
	}
}

// setCompletionHold đặt một Hold hoàn tất nhập; chỉ --continue mới bỏ qua (RFC §12.4). Lỗi bắt
// buộc lan truyền — Hold là bảo đảm duy nhất "sau nhập không viết tiếp nhầm", im lặng thất bại bằng
// bảo vệ tê liệt.
func (r *runner) setCompletionHold() error {
	in, err := r.ws.LoadIntent()
	if err != nil {
		return fmt.Errorf("đọc ý định nhập: %w", err)
	}
	if r.opts.ContinueAfter || (in != nil && in.ContinueAfterImport) {
		return nil
	}
	return r.deps.Store.RunMeta.SetAdvanceHold(domain.AdvanceHold{
		After:  domain.AdvanceHoldAtBoundary,
		Reason: "nhập tiểu thuyết ngoài hoàn tất, chờ nghiệm thu rồi viết tiếp",
	})
}
