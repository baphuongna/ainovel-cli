package host

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/models"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// recentSampleCap là kích thước cửa sổ trượt: chỉ giữ mẫu (cacheRead, input) của N lần gọi gần nhất theo role,
// để so sánh tỷ lệ hit "tích lũy vs gần N lần" ở cột trái, phân biệt "kéo lê giai đoạn đầu" với "tỷ lệ thấp ổn định".
const recentSampleCap = 10

// Hai ngưỡng phát hiện đứt gãy chuỗi cache (theo kinh nghiệm thực chứng của Claude Code): lượng hit giảm hơn
// 5% (tương đối) kèm mức giảm ≥2000 tokens (tuyệt đối) mới tính là đứt gãy — ngưỡng tương đối đơn lẻ sẽ bị nhiễu
// tiền tố nhỏ nhấn chìm, ngưỡng tuyệt đối đơn lẻ sẽ bỏ sót suy thoái rõ rệt của tiền tố lớn.
const (
	cacheBreakKeepRatio     = 0.95
	cacheBreakMinDropTokens = 2000
)

// UsageTracker tích lũy token vào/ra và chi phí USD của mọi agent trong suốt phiên.
//
// Cơ chế hoạt động:
//   - Mỗi lần callback OnMessage của agent chạy thì gọi Record(agentName, msg)
//   - agentName ánh xạ về role (architect_* chuẩn hóa thành architect), tra model mà ModelSet đang gán cho role đó
//   - Dùng models.DefaultRegistry tra giá model, nhân tích lũy theo bốn hạng mục input không cache/outputs/cache read/cache write
//   - Model không có trong registry thì lùi về msg.Usage.Cost.Total (provider tự mang, có thể là 0)
//   - Sau khi đổi model nóng (/model), tin nhắn sau tự động tính giá theo model mới, tin nhắn cũ giữ chi phí cũ
//
// Đồng thời duy trì chiều per-role (writer/editor/architect):
//   - dữ liệu hit tích lũy → hiệu quả tối ưu tổng thể
//   - cửa sổ trượt N lần gần nhất → phân biệt kéo lê giai đoạn đầu với tỷ lệ thấp ổn định
//   - cờ CacheCapable → phân biệt "chưa bật" với "thật sự 0% hit"
//
// Thread-safe.
type UsageTracker struct {
	mu       sync.Mutex
	overall  agentTotals
	perAgent map[string]*agentTotals // key là tên role sau khi chuẩn hóa agentRoleName
	perModel map[string]*agentTotals // key là provider/model; provider không rõ thì suy về model
	modelSet *bootstrap.ModelSet
	store    *storepkg.Store // có thể nil (kịch bản test), khi nil mọi method persist là noop âm thầm

	// cacheTrack là cơ sở chuỗi cache per-role (độ dài tiền tố/lượng hit/thời điểm lần gọi trước),
	// dùng cho phát hiện đứt gãy. Chỉ cập nhật trên đường Record live — replay lịch sử không dò,
	// nếu không mỗi lần khởi động đều đưa đứt gãy cũ thành báo nhầm. Không persist.
	cacheTrack map[string]*cacheTrackState

	// missingAssistantUsage đếm số lần "nhận được tin nhắn assistant nhưng Usage là nil".
	// Thực tế chủ yếu xảy ra khi backend tự dựng tương thích OpenAI không gửi final usage chunk ở cuối streaming
	// theo protocol stream_options.include_usage của OpenAI — partial.Usage luôn nil, mọi trường tích lũy
	// đứng yên ở 0. Bộ đếm giúp UI nói thẳng với người dùng "là upstream không trả usage, không phải bên này hỏng",
	// thay vì mò mã cache panel vô ích.
	missingAssistantUsage int
	loggedMissingUsage    bool // cả phiên chỉ warn một lần, tránh tui.log bị spam

	// saveCh do Record kích hoạt không khóa sau khi cộng dồn; autoSaveLoop lắng nghe và ghi đĩa theo debounce.
	// buffered=1: nhiều Record liên tiếp gập thành một tín hiệu ghi đĩa; đầy thì vứt, tick sau ghi chung.
	saveCh       chan struct{}
	autoSaveMu   sync.Mutex
	autoSaveDone chan struct{}

	// onCost được gọi ngoài khóa mang theo chi phí tích lũy mới nhất sau mỗi lần ghi sổ (BudgetSentinel dò vượt ngưỡng).
	// Phải được đặt qua SetOnCost trước khi Record đồng thời bắt đầu, sau đó chỉ đọc.
	onCost func(total float64)

	// onMissingUsage được gọi một lần khi lần đầu phát hiện "tin nhắn assistant không có Usage" (cùng thời điểm với
	// slog warn). Khi ngân sách đã bật, điều này nghĩa là vùng mù tính phí — chi phí luôn 0, ngân sách không bao giờ kích hoạt, phải báo động.
	onMissingUsage func()
}

// usageSample là mẫu hit của một lần OnMessage, chỉ ghi tử số và mẫu số của tỷ lệ hit.
type usageSample struct {
	CacheRead int
	Input     int
}

// cacheTrackState là cơ sở chuỗi cache của một role trong phiên hiện tại. task (văn bản nhiệm vụ spawn) là
// danh tính phiên: đổi task = spawn mới = dòng dõi cache mới (prompt_cache_key mang #seq), lần gọi đầu
// hit thấp là bình thường, đổi cơ sở luôn không so sánh — nếu không "phiên trước rất ngắn, tiền tố lần gọi đầu
// của phiên mới lại dài hơn" sẽ báo nhầm đứt gãy. Ngữ nghĩa Input (gồm CacheRead, xem comment computeCost)
// trùng khớp "độ dài tiền tố mà server xử lý", dựa đó phân biệt ba xu hướng: tiền tố ngắn lại = nén trong phiên
// (hợp lệ, reset cơ sở); tiền tố dài lên và hit tăng theo = chuỗi khỏe; tiền tố dài lên mà hit giảm mạnh = đứt gãy.
type cacheTrackState struct {
	task          string
	lastPrefix    int
	lastCacheRead int
	lastAt        time.Time
}

// agentTotals là bộ đếm tích lũy của một agent.
//   - Saved là khoản chênh lệch "nếu tính giá không cache" tính ngược từ dữ liệu hit hiện tại
//   - CacheCapable chỉ được đặt true sau khi role này ít nhất một lần chạy "model đã biết hỗ trợ cache"
//   - samples là ring buffer dài cố định, các lần đầu (recentSampleCap) append thẳng, sau đó xoay vòng theo sampleIdx
type agentTotals struct {
	Input        int
	Output       int
	CacheRead    int
	CacheWrite   int
	Cost         float64
	Saved        float64
	CacheCapable bool
	CacheBreaks  int // số lần đứt gãy chuỗi cache phát hiện trên live (replay không tính)
	samples      []usageSample
	sampleIdx    int
}

func NewUsageTracker(set *bootstrap.ModelSet, store *storepkg.Store) *UsageTracker {
	return &UsageTracker{
		modelSet:   set,
		store:      store,
		perAgent:   make(map[string]*agentTotals, 4),
		perModel:   make(map[string]*agentTotals, 4),
		cacheTrack: make(map[string]*cacheTrackState, 4),
		saveCh:     make(chan struct{}, 1),
	}
}

// Record phân phối một tin nhắn agent vào hai đường tích lũy / chẩn đoán.
//
// Tích lũy chỉ nhìn việc Usage có tồn tại — "tin nhắn nào mang Usage" là chi tiết lắp ráp của adapter
// agentcore/litellm (protocol upstream đặt usage ở tầng cao của response), sau này luật lắp ráp đổi
// cũng không phải sửa đây. Chẩn đoán yêu cầu Role=Assistant và Content khác rỗng, tránh AbortMsg /
// khôi phục bất thường / tin nhắn tool / user làm bẩn bộ đếm missingAssistantUsage.
func (t *UsageTracker) Record(agentName, task string, msg agentcore.AgentMessage) {
	if t == nil {
		return
	}
	m, ok := msg.(agentcore.Message)
	if !ok {
		return
	}
	if m.Usage == nil {
		if m.Role == agentcore.RoleAssistant && len(m.Content) > 0 {
			t.flagMissingUsage(agentName)
		}
		return
	}
	role := agentRoleName(agentName)
	t.noteCacheBreak(role, task, *m.Usage)
	provider, modelName := usageActualModel(m.Usage)
	t.accumulate(role, provider, modelName, *m.Usage)
}

// noteCacheBreak là phát hiện đứt gãy chuỗi cache (thuần quan sát, không sửa, chỉ gọi trên đường Record live).
//
// Xét: trong cùng phiên (role+task) tiền tố (Input, gồm CacheRead) không ngắn lại, mà lượng hit so lần trước
// giảm >5% và mức giảm ≥2000 tokens. Task đổi = spawn mới = dòng dõi cache mới, đổi cơ sở luôn không
// so sánh; tiền tố ngắn lại nghĩa là nén ngữ cảnh, thuộc giảm hợp lệ, chỉ reset cơ sở không cảnh báo. Quy
// kết theo ưu tiên gợi ý: khoảng cách vượt TTL → nghi hết hạn; khoảng cách rất ngắn mà byte client lẽ ra
// ổn định → nghi server đẩy ra / trôi route (trạm trung chuyển luân phiên upstream là nguyên nhân phổ biến).
func (t *UsageTracker) noteCacheBreak(role, task string, u agentcore.Usage) {
	now := time.Now()
	prefix := u.Input // litellm đảm bảo Input gồm CacheRead với mọi provider

	t.mu.Lock()
	st := t.cacheTrack[role]
	if st == nil || st.task != task {
		t.cacheTrack[role] = &cacheTrackState{task: task, lastPrefix: prefix, lastCacheRead: u.CacheRead, lastAt: now}
		t.mu.Unlock()
		return
	}
	prevPrefix, prevRead, prevAt := st.lastPrefix, st.lastCacheRead, st.lastAt
	st.lastPrefix, st.lastCacheRead, st.lastAt = prefix, u.CacheRead, now

	broke := prevPrefix > 0 && prefix >= prevPrefix &&
		float64(u.CacheRead) < float64(prevRead)*cacheBreakKeepRatio &&
		prevRead-u.CacheRead >= cacheBreakMinDropTokens
	if broke {
		t.overall.CacheBreaks++
		per := t.perAgent[role]
		if per == nil {
			per = &agentTotals{}
			t.perAgent[role] = per
		}
		per.CacheBreaks++
	}
	t.mu.Unlock()

	if !broke {
		return
	}
	gap := now.Sub(prevAt).Round(time.Second)
	hint := "nghi server đẩy ra / trôi route (trạm trung chuyển luân phiên upstream là nguyên nhân phổ biến)"
	if gap > time.Hour {
		hint = "nghi hết hạn TTL 1h"
	} else if gap > 5*time.Minute {
		hint = "nghi hết hạn TTL 5m"
	}
	slog.Warn("Đứt gãy chuỗi cache: tiền tố không ngắn lại mà hit giảm mạnh",
		"module", "usage", "role", role,
		"cache_read", fmt.Sprintf("%d→%d", prevRead, u.CacheRead),
		"prefix", fmt.Sprintf("%d→%d", prevPrefix, prefix),
		"gap", gap.String(), "hint", hint)
	t.notifyDirty()
}

func usageActualModel(u *agentcore.Usage) (provider, modelName string) {
	if u == nil {
		return "", ""
	}
	return strings.TrimSpace(u.Provider), strings.TrimSpace(u.Model)
}

// flagMissingUsage đếm một sự kiện "nhìn như response LLM thật mà không lấy được usage", cả phiên chỉ in
// một dòng warn tránh tui.log bị spam.
func (t *UsageTracker) flagMissingUsage(agentName string) {
	t.mu.Lock()
	t.missingAssistantUsage++
	shouldLog := !t.loggedMissingUsage
	t.loggedMissingUsage = true
	t.mu.Unlock()
	if shouldLog {
		slog.Warn("Response LLM không mang dữ liệu usage, panel cache/chi phí sẽ không có tích lũy — thường là upstream streaming không gửi final usage chunk theo protocol include_usage của OpenAI",
			"module", "usage", "agent", agentName)
		if t.onMissingUsage != nil {
			t.onMissingUsage()
		}
	}
	t.notifyDirty()
}

// SetOnMissingUsage đăng ký callback một lần "lần đầu phát hiện thiếu usage".
// Phải được gọi một lần trong giai đoạn dựng Host, trước khi Record đồng thời bắt đầu.
func (t *UsageTracker) SetOnMissingUsage(cb func()) {
	if t == nil {
		return
	}
	t.onMissingUsage = cb
}

// notifyDirty kích hoạt không khóa một tín hiệu ghi đĩa, do autoSaveLoop ghi thật theo debounce.
// Kênh tín hiệu buffered=1: nhiều Record liên tiếp gập thành một yêu cầu lưu là đủ.
func (t *UsageTracker) notifyDirty() {
	if t == nil || t.saveCh == nil {
		return
	}
	select {
	case t.saveCh <- struct{}{}:
	default:
	}
}

// accumulate cộng dồn một tin nhắn có Usage vào ba bộ đếm overall / per-role / per-model.
// provider/model rỗng nghĩa là "dùng ModelSet hiện tại lấy model của role" (đường live); khác rỗng nghĩa là
// "ép tính giá theo model chỉ định" (đường replay dùng _meta trong session jsonl).
// resolveCost chạy ngoài khóa (nó chỉ đọc modelSet/Registry), trong khóa chỉ làm phép cộng.
func (t *UsageTracker) accumulate(role, provider, modelName string, u agentcore.Usage) {
	provider, modelName = t.effectiveModel(role, provider, modelName)
	cost, saved, capable := t.resolveCost(modelName, u)

	t.mu.Lock()
	addUsage(&t.overall, u, cost, saved, capable)

	per := t.perAgent[role]
	if per == nil {
		per = &agentTotals{}
		t.perAgent[role] = per
	}
	addUsage(per, u, cost, saved, capable)

	if key := modelUsageKey(provider, modelName); key != "" {
		perModel := t.perModel[key]
		if perModel == nil {
			perModel = &agentTotals{}
			t.perModel[key] = perModel
		}
		addUsage(perModel, u, cost, saved, capable)
	}
	total := t.overall.Cost
	t.mu.Unlock()

	t.notifyDirty()
	if t.onCost != nil {
		t.onCost(total)
	}
}

// SetOnCost đăng ký callback ghi sổ (mang chi phí tích lũy mới nhất, gọi ngoài khóa).
// Phải được gọi một lần trong giai đoạn dựng Host, trước khi Record đồng thời bắt đầu.
func (t *UsageTracker) SetOnCost(cb func(total float64)) {
	if t == nil {
		return
	}
	t.onCost = cb
}

func (t *UsageTracker) effectiveModel(role, provider, modelName string) (string, string) {
	provider = strings.TrimSpace(provider)
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		if t != nil && t.modelSet != nil {
			p, m, _ := t.modelSet.CurrentSelection(role)
			return p, m
		}
		return "", ""
	}
	if provider == "" && t != nil && t.modelSet != nil {
		p, m, _ := t.modelSet.CurrentSelection(role)
		if m == modelName {
			provider = p
		}
	}
	return provider, modelName
}

func modelUsageKey(provider, modelName string) string {
	provider = strings.TrimSpace(provider)
	modelName = strings.TrimSpace(modelName)
	switch {
	case modelName == "":
		return ""
	case provider == "":
		return modelName
	default:
		return provider + "/" + modelName
	}
}

// addUsage cộng dồn token và chi phí của một lần gọi vào một bản totals.
// Phải được gọi khi đang giữ UsageTracker.mu.
//
// CacheCapable ưu tiên xét theo "dữ kiện": chỉ cần từng thấy CacheRead hoặc CacheWrite > 0 là chứng minh
// upstream đã thật sự làm prompt caching. CacheReadCostPer1M của registry chỉ làm fallback,
// vì model của backend tự dựng (mimo-v2.5-pro / proxy nội địa v.v.) thường không có trong index giá của
// BerriAI/litellm, nhưng Usage thực tế hoàn toàn có dữ liệu cache, UI không nên đoán nhầm là "chưa bật".
func addUsage(t *agentTotals, u agentcore.Usage, cost, saved float64, capable bool) {
	t.Input += u.Input
	t.Output += u.Output
	t.CacheRead += u.CacheRead
	t.CacheWrite += u.CacheWrite
	t.Cost += cost
	t.Saved += saved
	if capable || u.CacheRead > 0 || u.CacheWrite > 0 {
		t.CacheCapable = true
	}
	pushSample(t, u.CacheRead, u.Input)
}

// pushSample đẩy một mẫu vào ring buffer. Các lần đầu (recentSampleCap) thuần append, sau đó xoay vòng ghi đè.
func pushSample(t *agentTotals, cacheRead, input int) {
	s := usageSample{CacheRead: cacheRead, Input: input}
	if len(t.samples) < recentSampleCap {
		t.samples = append(t.samples, s)
		return
	}
	t.samples[t.sampleIdx] = s
	t.sampleIdx = (t.sampleIdx + 1) % recentSampleCap
}

// recentSums trả tổng cacheRead và input trong cửa sổ trượt, làm tử số và mẫu số của "tỷ lệ hit gần N lần".
// Dùng sum/sum thay vì "trung bình tỷ lệ từng lần" để tránh mẫu nhỏ (input vài trăm token) khuyếch đại nhiễu.
func recentSums(t *agentTotals) (cacheRead, input int) {
	for _, s := range t.samples {
		cacheRead += s.CacheRead
		input += s.Input
	}
	return cacheRead, input
}

// Totals trả snapshot tổng tích lũy.
func (t *UsageTracker) Totals() (cost float64, input, output, cacheRead, cacheWrite int) {
	if t == nil {
		return 0, 0, 0, 0, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.overall.Cost, t.overall.Input, t.overall.Output, t.overall.CacheRead, t.overall.CacheWrite
}

// SavedUSD trả tổng USD tiết kiệm được nhờ hit cache.
func (t *UsageTracker) SavedUSD() float64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.overall.Saved
}

// OverallRecent trả tổng cacheRead, tổng input và số mẫu trong cửa sổ trượt (≤ recentSampleCap lần).
func (t *UsageTracker) OverallRecent() (cacheRead, input, samples int) {
	if t == nil {
		return 0, 0, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	r, in := recentSums(&t.overall)
	return r, in, len(t.overall.samples)
}

// OverallCacheBreaks trả tổng số lần đứt gãy chuỗi cache phát hiện trên live.
func (t *UsageTracker) OverallCacheBreaks() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.overall.CacheBreaks
}

// OverallCacheCapable: tổng thể có ít nhất một lần chạy model đã biết hỗ trợ cache hay không.
func (t *UsageTracker) OverallCacheCapable() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.overall.CacheCapable
}

// MissingAssistantUsage trả số lần tích lũy "nhận tin nhắn assistant nhưng Usage là nil".
// Lớn hơn 0 thường nghĩa là upstream streaming không gửi final usage chunk của OpenAI,
// UI dựa đó hiển thị gợi ý thay vì tưởng nhầm module cache tự nó hỏng.
func (t *UsageTracker) MissingAssistantUsage() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.missingAssistantUsage
}

// ── Persist ──

// Snapshot chép trạng thái tích lũy hiện tại thành domain.UsageState khả dụng serialize.
// Cửa sổ trượt samples không vào snapshot — nó là cửa sổ chẩn đoán ngắn hạn, ít ý nghĩa xuyên tiến trình.
func (t *UsageTracker) Snapshot() domain.UsageState {
	if t == nil {
		return domain.UsageState{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	state := domain.UsageState{
		Schema:       domain.UsageSchemaVersion,
		UpdatedAt:    time.Now(),
		Overall:      totalsSnapshot(&t.overall),
		PerAgent:     make(map[string]domain.AgentUsageTotals, len(t.perAgent)),
		PerModel:     make(map[string]domain.AgentUsageTotals, len(t.perModel)),
		MissingUsage: t.missingAssistantUsage,
	}
	for role, v := range t.perAgent {
		state.PerAgent[role] = totalsSnapshot(v)
	}
	for model, v := range t.perModel {
		state.PerModel[model] = totalsSnapshot(v)
	}
	return state
}

// LoadFromStore đọc snapshot đã persist từ store.Usage và đổ lại vào bộ nhớ. Trả true nghĩa là
// tải được một trạng thái khác rỗng (schema khớp); false nghĩa là không có file hoặc không dùng được,
// bên gọi nên tiếp tục đi backfill một lần từ session replay.
func (t *UsageTracker) LoadFromStore() (bool, error) {
	if t == nil || t.store == nil {
		return false, nil
	}
	state, err := t.store.Usage.Load()
	if err != nil {
		return false, err
	}
	if state == nil {
		return false, nil
	}
	t.applyState(*state)
	return true, nil
}

// SaveNow ghi đĩa ngay snapshot hiện tại. Cả đường autoSaveLoop / Close đều ghi qua nó.
func (t *UsageTracker) SaveNow() error {
	if t == nil || t.store == nil {
		return nil
	}
	return t.store.Usage.Save(t.Snapshot())
}

// StartAutoSave chạy một goroutine, lắng nghe saveCh + ghi đĩa theo debounce. Trước khi ctx done sẽ
// flush lần cuối trạng thái chưa lưu. Close kích hoạt flush + thoát bằng cách cancel ctx.
func (t *UsageTracker) StartAutoSave(ctx context.Context) {
	if t == nil || t.store == nil {
		return
	}
	done := make(chan struct{})
	t.autoSaveMu.Lock()
	t.autoSaveDone = done
	t.autoSaveMu.Unlock()
	go func() {
		defer close(done)
		t.autoSaveLoop(ctx)
	}()
}

// WaitAutoSave đợi lần flush cuối sau khi cancel hoàn tất. Host.Close gọi cancel trước,
// rồi đợi ở đây, tránh autoSaveLoop và SaveNow trước khi thoát ghi cùng một snapshot đồng thời.
func (t *UsageTracker) WaitAutoSave() {
	if t == nil {
		return
	}
	t.autoSaveMu.Lock()
	done := t.autoSaveDone
	t.autoSaveMu.Unlock()
	if done != nil {
		<-done
	}
}

// autoSaveLoop tiết lưu tín hiệu dirty tần suất cao thành ghi đĩa 500ms một lần.
//
// Ghi chú thiết kế: 500ms là giá kinh nghiệm — mỗi chương 1-2 lượt LLM, ghi đĩa 1-2 lần hoàn toàn chấp nhận được;
// kể cả người dùng thoát bằng ctrl+C kịp không kích hoạt timer, đường cancel ctx cũng sẽ flush lần cuối.
// Crash thật sự (OS kill -9) sẽ mất tích lũy trong 0.5s gần nhất — session jsonl phía upstream vẫn là
// dữ kiện đầy đủ, lần khởi động sau sẽ replay từ sessions/ để bù khoản chênh lệch.
func (t *UsageTracker) autoSaveLoop(ctx context.Context) {
	const debounce = 500 * time.Millisecond
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()

	var pending bool
	flush := func() {
		if err := t.SaveNow(); err != nil {
			slog.Warn("Ghi đĩa usage thất bại", "module", "usage", "err", err)
		}
		pending = false
	}
	for {
		select {
		case <-ctx.Done():
			if pending {
				flush()
			}
			return
		case <-t.saveCh:
			if pending {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
			timer.Reset(debounce)
			pending = true
		case <-timer.C:
			flush()
		}
	}
}

// applyState ghi snapshot đã persist ngược vào bộ nhớ. Chỉ gọi lúc khởi động (sau LoadFromStore / replay),
// lúc này chưa khởi động autoSaveLoop / Record cũng không kích hoạt đồng thời, có thể không giữ khóa;
// nhưng vẫn giữ mu phòng khi test hoặc thứ tự gọi tương lai đổi đưa vào đồng thời.
func (t *UsageTracker) applyState(state domain.UsageState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.overall = totalsFromState(state.Overall)
	if state.PerAgent == nil {
		t.perAgent = make(map[string]*agentTotals, 4)
	} else {
		t.perAgent = make(map[string]*agentTotals, len(state.PerAgent))
		for role, v := range state.PerAgent {
			tot := totalsFromState(v)
			t.perAgent[role] = &tot
		}
	}
	if state.PerModel == nil {
		t.perModel = make(map[string]*agentTotals, 4)
	} else {
		t.perModel = make(map[string]*agentTotals, len(state.PerModel))
		for model, v := range state.PerModel {
			tot := totalsFromState(v)
			t.perModel[model] = &tot
		}
	}
	t.missingAssistantUsage = state.MissingUsage
}

// totalsSnapshot chép agentTotals trong bộ nhớ thành domain.AgentUsageTotals khả dụng persist.
// Ring buffer samples cố tình không mang ra — xem comment UsageState.
func totalsSnapshot(t *agentTotals) domain.AgentUsageTotals {
	if t == nil {
		return domain.AgentUsageTotals{}
	}
	return domain.AgentUsageTotals{
		Input:        t.Input,
		Output:       t.Output,
		CacheRead:    t.CacheRead,
		CacheWrite:   t.CacheWrite,
		Cost:         t.Cost,
		Saved:        t.Saved,
		CacheCapable: t.CacheCapable,
		CacheBreaks:  t.CacheBreaks,
	}
}

// totalsFromState phục hồi dạng persist về agentTotals trong bộ nhớ. Samples để rỗng, sau khởi động lại
// tích lũy lại từ 0, sau vài lần Record là khôi phục ngữ nghĩa "tỷ lệ hit gần N lần".
func totalsFromState(s domain.AgentUsageTotals) agentTotals {
	return agentTotals{
		Input:        s.Input,
		Output:       s.Output,
		CacheRead:    s.CacheRead,
		CacheWrite:   s.CacheWrite,
		Cost:         s.Cost,
		Saved:        s.Saved,
		CacheCapable: s.CacheCapable,
		CacheBreaks:  s.CacheBreaks,
	}
}

// AgentUsage là snapshot lượng dùng tích lũy của một agent (phơi ra cho UI).
type AgentUsage struct {
	Role            string
	Model           string
	Input           int
	Output          int
	CacheRead       int
	CacheWrite      int
	Cost            float64
	Saved           float64
	CacheCapable    bool
	RecentCacheRead int
	RecentInput     int
	RecentSamples   int
}

// PerAgent trả lượng dùng tích lũy các role. Kết quả giảm dần theo CacheRead, bỏ qua role chưa tiêu thụ token.
func (t *UsageTracker) PerAgent() []AgentUsage {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]AgentUsage, 0, len(t.perAgent))
	for role, v := range t.perAgent {
		if v.Input == 0 && v.Output == 0 {
			continue
		}
		recentRead, recentInput := recentSums(v)
		out = append(out, AgentUsage{
			Role:            role,
			Input:           v.Input,
			Output:          v.Output,
			CacheRead:       v.CacheRead,
			CacheWrite:      v.CacheWrite,
			Cost:            v.Cost,
			Saved:           v.Saved,
			CacheCapable:    v.CacheCapable,
			RecentCacheRead: recentRead,
			RecentInput:     recentInput,
			RecentSamples:   len(v.samples),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CacheRead != out[j].CacheRead {
			return out[i].CacheRead > out[j].CacheRead
		}
		return out[i].Input > out[j].Input
	})
	return out
}

// PerModel trả lượng dùng tích lũy các model. Kết quả giảm dần theo chi phí, kế đến theo lượng input.
func (t *UsageTracker) PerModel() []AgentUsage {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]AgentUsage, 0, len(t.perModel))
	for model, v := range t.perModel {
		if v.Input == 0 && v.Output == 0 {
			continue
		}
		out = append(out, AgentUsage{
			Model:        model,
			Input:        v.Input,
			Output:       v.Output,
			CacheRead:    v.CacheRead,
			CacheWrite:   v.CacheWrite,
			Cost:         v.Cost,
			Saved:        v.Saved,
			CacheCapable: v.CacheCapable,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		return out[i].Input > out[j].Input
	})
	return out
}

// resolveCost đồng thời trả cost / saved / capable của tin nhắn này.
//   - cost: khớp registry thì nhân theo 4 hạng mục; không khớp thì lùi về cost provider tự mang
//   - saved: chỉ > 0 khi khớp registry, CacheRead > 0, và InputCost > CacheReadCost
//   - capable: khớp registry và CacheReadCostPer1M của model đó > 0 → đã biết hỗ trợ prompt caching
//
// modelName ưu tiên dùng giá trị bên gọi truyền vào (khi replay lấy từ _meta.model của session jsonl).
func (t *UsageTracker) resolveCost(modelName string, u agentcore.Usage) (cost, saved float64, capable bool) {
	if entry, ok := models.DefaultRegistry().Resolve(modelName); ok {
		c := computeCost(u, *entry)
		s := computeSaved(u, *entry)
		canCache := entry.CacheReadCostPer1M > 0
		if c > 0 {
			return c, s, canCache
		}
	}
	if u.Cost != nil {
		return u.Cost.Total, 0, false
	}
	return 0, 0, false
}

// agentRoleName chuẩn hóa tên subagent về tên role.
// architect_short/mid/long đều về architect; các tên khác trả nguyên văn.
func agentRoleName(agentName string) string {
	if strings.HasPrefix(agentName, "architect_") {
		return "architect"
	}
	return agentName
}

// computeCost tính chi phí USD của lần gọi theo đơn giá $/1M tokens.
//
// Tiền đề ngữ nghĩa (do các provider của litellm đảm bảo thống nhất, xem điểm lắp Usage trong
// anthropic.go / bedrock.go / openai.go / gemini.go / compat.go):
//
//	u.Input  = toàn bộ input token, **gồm** CacheRead; không gồm CacheWrite
//	u.Output = output token
//
// Do đó nonCachedInput = u.Input - u.CacheRead đúng với mọi provider.
// Nhánh fallback giữ lại là để đối phó khi một provider nào đó trong tương lai trả dữ liệu bẩn cũng không đổ.
func computeCost(u agentcore.Usage, e models.ModelEntry) float64 {
	nonCachedInput := u.Input - u.CacheRead
	if nonCachedInput < 0 {
		nonCachedInput = u.Input
	}
	c := 0.0
	c += float64(nonCachedInput) * e.InputCostPer1M / 1_000_000
	c += float64(u.Output) * e.OutputCostPer1M / 1_000_000
	c += float64(u.CacheRead) * e.CacheReadCostPer1M / 1_000_000
	c += float64(u.CacheWrite) * e.CacheWriteCostPer1M / 1_000_000
	return c
}

// computeSaved ước lượng USD tiết kiệm được từ CacheRead so với "tính giá input thường".
// Lưu ý phần premium của CacheWrite không khấu trừ — nó là khoản đầu tư cần thiết "lót đường cho các lần hit
// sau", lợi nhuận thật nhờ CacheRead các lần sau thu hồi dần.
func computeSaved(u agentcore.Usage, e models.ModelEntry) float64 {
	if u.CacheRead <= 0 || e.InputCostPer1M <= 0 {
		return 0
	}
	delta := e.InputCostPer1M - e.CacheReadCostPer1M
	if delta <= 0 {
		return 0
	}
	return float64(u.CacheRead) * delta / 1_000_000
}
