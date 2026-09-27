package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/models"
)

func TestUsageTrackerReplaySessionsReadsWorkerLogs(t *testing.T) {
	dir := t.TempDir()
	sessionsDir := filepath.Join(dir, "meta", "sessions", "agents")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	rec := sessionRecord{
		Role: agentcore.RoleAssistant,
		Usage: &agentcore.Usage{
			Input: 1200, Output: 300, CacheRead: 800,
		},
		Meta: &sessionRecordMeta{Provider: "openrouter", Model: "test-model"},
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(sessionsDir, "writer-ch01.jsonl"), data, 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}

	tk := NewUsageTracker(nil, nil)
	n, err := tk.ReplaySessions(dir)
	if err != nil {
		t.Fatalf("ReplaySessions: %v", err)
	}
	if n != 1 {
		t.Fatalf("replayed messages = %d, want 1", n)
	}
	_, input, output, cacheRead, _ := tk.Totals()
	if input != 1200 || output != 300 || cacheRead != 800 {
		t.Fatalf("replayed totals = input:%d output:%d cache:%d", input, output, cacheRead)
	}
}

// makeUsageMsg dựng một tin nhắn mà callback OnMessage chấp nhận được (kèm Usage).
// Role phải đặt tường minh thành assistant: UsageTracker.Record giờ lọc theo vai trò,
// chỉ tin nhắn assistant mới được tích lũy (vai trò khác đương nhiên không mang usage).
func makeUsageMsg(input, cacheRead, cacheWrite, output int) agentcore.AgentMessage {
	return agentcore.Message{
		Role: agentcore.RoleAssistant,
		Usage: &agentcore.Usage{
			Input: input, Output: output, CacheRead: cacheRead, CacheWrite: cacheWrite,
		},
	}
}

// Test_pushSample_RingBuffer kiểm chứng ngữ nghĩa xoay vòng của cửa sổ trượt:
// N lần đầu append thẳng; sau đó ghi đè mẫu cũ nhất theo sampleIdx. recentSums luôn phản ánh "N lần gần nhất".
func Test_pushSample_RingBuffer(t *testing.T) {
	var tot agentTotals

	for i := 1; i <= recentSampleCap; i++ {
		pushSample(&tot, i, i*100)
	}
	if got := len(tot.samples); got != recentSampleCap {
		t.Fatalf("after %d pushes, samples len=%d want %d", recentSampleCap, got, recentSampleCap)
	}

	pushSample(&tot, 999, 99900)
	if got := len(tot.samples); got != recentSampleCap {
		t.Fatalf("after overflow, samples len=%d want %d (no growth)", got, recentSampleCap)
	}
	cacheRead, input := recentSums(&tot)
	expectedCacheRead := 999
	expectedInput := 99900
	for i := 2; i <= recentSampleCap; i++ {
		expectedCacheRead += i
		expectedInput += i * 100
	}
	if cacheRead != expectedCacheRead || input != expectedInput {
		t.Fatalf("recentSums after overflow = (%d, %d), want (%d, %d)",
			cacheRead, input, expectedCacheRead, expectedInput)
	}
}

// Test_UsageTracker_RecordAccumulates kiểm chứng Record tích lũy nhiều role đúng,
// hợp nhất tổng thể = tổng mọi role; per-role độc lập từng cái.
func Test_UsageTracker_RecordAccumulates(t *testing.T) {
	tk := NewUsageTracker(nil, nil) // modelSet=nil → đi fallback Cost của provider, không ảnh hưởng logic tích lũy

	tk.Record("writer", "", makeUsageMsg(1000, 800, 0, 200))
	tk.Record("writer", "", makeUsageMsg(1500, 1200, 100, 300))
	tk.Record("editor", "", makeUsageMsg(500, 0, 0, 100))

	cost, in, out, cr, cw := tk.Totals()
	if in != 3000 || out != 600 || cr != 2000 || cw != 100 {
		t.Fatalf("totals = (in=%d out=%d cr=%d cw=%d), want (3000 600 2000 100)", in, out, cr, cw)
	}
	if cost != 0 {
		t.Errorf("cost should be 0 when modelSet=nil and no provider Cost, got %v", cost)
	}

	per := tk.PerAgent()
	if len(per) != 2 {
		t.Fatalf("per-agent len=%d want 2", len(per))
	}
	// PerAgent giảm dần theo CacheRead: writer (2000) phải đứng trước editor (0)
	if per[0].Role != "writer" || per[1].Role != "editor" {
		t.Fatalf("per-agent order = %s,%s want writer,editor", per[0].Role, per[1].Role)
	}
	if per[0].Input != 2500 || per[0].CacheRead != 2000 {
		t.Errorf("writer totals = (in=%d cr=%d), want (2500 2000)", per[0].Input, per[0].CacheRead)
	}
}

// Test_UsageTracker_ArchitectAliasNormalized kiểm chứng architect_short/mid/long
// đều chuẩn hóa về cùng key "architect" (tránh bị role con do /model chuyển tách thành nhiều dòng).
func Test_UsageTracker_ArchitectAliasNormalized(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	tk.Record("architect_short", "", makeUsageMsg(100, 50, 0, 20))
	tk.Record("architect_mid", "", makeUsageMsg(200, 100, 0, 40))
	tk.Record("architect_long", "", makeUsageMsg(300, 150, 0, 60))

	per := tk.PerAgent()
	if len(per) != 1 {
		t.Fatalf("aliases must merge to single role, got %d entries: %+v", len(per), per)
	}
	if per[0].Role != "architect" {
		t.Fatalf("merged role name = %q, want architect", per[0].Role)
	}
	if per[0].Input != 600 || per[0].CacheRead != 300 {
		t.Errorf("merged totals = (in=%d cr=%d), want (600 300)", per[0].Input, per[0].CacheRead)
	}
}

func Test_UsageTracker_PerModelAccumulates(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	tk.accumulate("writer", "openrouter", "model-a", agentcore.Usage{Input: 1000, Output: 200, CacheRead: 700})
	tk.accumulate("editor", "openrouter", "model-b", agentcore.Usage{Input: 500, Output: 100})
	tk.accumulate("writer", "openrouter", "model-a", agentcore.Usage{Input: 300, Output: 80, CacheRead: 200})

	perModel := tk.PerModel()
	if len(perModel) != 2 {
		t.Fatalf("per-model len=%d want 2", len(perModel))
	}
	seen := map[string]AgentUsage{}
	for _, m := range perModel {
		seen[m.Model] = m
	}
	if seen["openrouter/model-a"].Input != 1300 || seen["openrouter/model-a"].CacheRead != 900 {
		t.Errorf("model-a totals = %+v", seen["openrouter/model-a"])
	}
	if seen["openrouter/model-b"].Output != 100 {
		t.Errorf("model-b totals = %+v", seen["openrouter/model-b"])
	}

	snap := tk.Snapshot()
	restored := NewUsageTracker(nil, nil)
	restored.applyState(snap)
	if got := restored.PerModel(); len(got) != 2 {
		t.Fatalf("restored per-model len=%d want 2: %+v", len(got), got)
	}
}

func Test_UsageTracker_RecordUsesActualUsageModel(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	tk.Record("writer", "", agentcore.Message{
		Role: agentcore.RoleAssistant,
		Usage: &agentcore.Usage{
			Provider: "openrouter",
			Model:    "google/gemini-2.5-pro",
			Input:    1000,
			Output:   200,
		},
	})

	perModel := tk.PerModel()
	if len(perModel) != 1 {
		t.Fatalf("per-model len=%d want 1: %+v", len(perModel), perModel)
	}
	if perModel[0].Model != "openrouter/google/gemini-2.5-pro" {
		t.Fatalf("model key = %q, want openrouter/google/gemini-2.5-pro", perModel[0].Model)
	}
	if perModel[0].Input != 1000 || perModel[0].Output != 200 {
		t.Fatalf("model totals = %+v", perModel[0])
	}
}

func Test_UsageTracker_ProviderOnlyDoesNotInventModelKey(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	tk.Record("writer", "", agentcore.Message{
		Role: agentcore.RoleAssistant,
		Usage: &agentcore.Usage{
			Provider: "openrouter",
			Input:    1000,
			Output:   200,
		},
	})

	if got := tk.PerModel(); len(got) != 0 {
		t.Fatalf("provider-only usage must not create model stats without a model, got %+v", got)
	}
}

// Test_UsageTracker_RecentWindowReflectsLatest kiểm chứng cửa sổ trượt phản ánh "N lần gần nhất",
// không bị kéo lê bởi hit thấp giai đoạn đầu — đúng vấn đề "kéo lê đầu kỳ vs hit thấp ổn định" mà P1 cần giải quyết.
func Test_UsageTracker_RecentWindowReflectsLatest(t *testing.T) {
	tk := NewUsageTracker(nil, nil)

	// 5 lần đầu hit cực thấp (cảnh chương đầu)
	for i := 0; i < 5; i++ {
		tk.Record("writer", "", makeUsageMsg(1000, 0, 0, 200))
	}
	// 8 lần sau (>5) hit cao (cảnh ổn định)
	for i := 0; i < 8; i++ {
		tk.Record("writer", "", makeUsageMsg(1000, 900, 0, 200))
	}

	per := tk.PerAgent()
	if len(per) != 1 {
		t.Fatalf("len=%d want 1", len(per))
	}
	w := per[0]

	// Tích lũy: 13 lần có 8 lần hit → 7200/13000 ≈ 55.4%
	cumulativeRate := float64(w.CacheRead) / float64(w.Input) * 100
	if cumulativeRate < 50 || cumulativeRate > 60 {
		t.Errorf("cumulative hit rate = %.1f%%, want ~55%%", cumulativeRate)
	}

	// Cửa sổ trượt: 10 lần gần nhất có 8 lần hit cao + 2 lần hit 0 → 7200/10000 = 72%
	if w.RecentSamples != recentSampleCap {
		t.Errorf("recent samples = %d, want %d (window full)", w.RecentSamples, recentSampleCap)
	}
	recentRate := float64(w.RecentCacheRead) / float64(w.RecentInput) * 100
	if recentRate < 70 || recentRate > 75 {
		t.Errorf("recent hit rate = %.1f%%, want ~72%% (proves window dropped early misses)", recentRate)
	}
	// Điểm mấu chốt: gần N lần cao rõ rệt so với tích lũy, chứng minh các lần 0 thời kỳ đầu đã bị vứt khỏi cửa sổ
	if recentRate <= cumulativeRate {
		t.Errorf("recent (%.1f%%) must exceed cumulative (%.1f%%) once window slides past early misses",
			recentRate, cumulativeRate)
	}
}

// Test_computeSaved kiểm chứng thuật toán saved: CacheRead × (giá Input - giá CacheRead);
// chênh giá ≤ 0 hoặc InputCost ≤ 0 thì trả 0 (phần premium CacheWrite không khấu trừ).
func Test_computeSaved(t *testing.T) {
	cases := []struct {
		name  string
		usage agentcore.Usage
		entry models.ModelEntry
		want  float64
	}{
		{
			name:  "anthropic 5m hit tiết kiệm 90%",
			usage: agentcore.Usage{Input: 100_000, CacheRead: 80_000},
			entry: models.ModelEntry{InputCostPer1M: 3.0, CacheReadCostPer1M: 0.3},
			want:  80_000 * (3.0 - 0.3) / 1_000_000, // 0.216
		},
		{
			name:  "không hit saved=0",
			usage: agentcore.Usage{Input: 100_000, CacheRead: 0},
			entry: models.ModelEntry{InputCostPer1M: 3.0, CacheReadCostPer1M: 0.3},
			want:  0,
		},
		{
			name:  "model không có giá saved=0",
			usage: agentcore.Usage{Input: 100_000, CacheRead: 50_000},
			entry: models.ModelEntry{InputCostPer1M: 0, CacheReadCostPer1M: 0},
			want:  0,
		},
		{
			name:  "chênh giá bất thường saved=0",
			usage: agentcore.Usage{Input: 100_000, CacheRead: 50_000},
			entry: models.ModelEntry{InputCostPer1M: 1.0, CacheReadCostPer1M: 2.0}, // cache lại đắt hơn
			want:  0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeSaved(tc.usage, tc.entry)
			if got != tc.want {
				t.Errorf("computeSaved=%v want %v", got, tc.want)
			}
		})
	}
}

// Test_UsageTracker_CacheCapableSticky kiểm chứng CacheCapable một khi đặt true thì không lùi.
// Đã từng chạy model hỗ trợ cache → dữ liệu hit tích lũy có hiệu lực; giữa chặng chuyển sang model không hỗ trợ không được làm cờ lùi.
//
// Mô phỏng bằng cách gán trực tiếp perAgent (đường resolveCost cần ModelSet+Registry, tầng tích hợp đã phủ).
func Test_UsageTracker_CacheCapableSticky(t *testing.T) {
	tk := NewUsageTracker(nil, nil)

	// Mô phỏng "từng chạy model hỗ trợ cache + từng hit"
	tk.perAgent["writer"] = &agentTotals{
		Input: 1000, CacheRead: 500, Output: 200, CacheCapable: true,
	}
	// Sau đó bổ sung một lần "gọi model không hỗ trợ cache"
	tk.Record("writer", "", makeUsageMsg(500, 0, 0, 100))

	per := tk.PerAgent()
	if len(per) != 1 || per[0].Role != "writer" {
		t.Fatalf("expected single writer entry, got %+v", per)
	}
	if !per[0].CacheCapable {
		t.Errorf("CacheCapable must remain true after switching to non-capable model")
	}
	if per[0].CacheRead != 500 || per[0].Input != 1500 {
		t.Errorf("totals after merge = (in=%d cr=%d), want (1500 500)",
			per[0].Input, per[0].CacheRead)
	}
}

// Test_UsageTracker_PerAgentSkipsZero kiểm chứng role chưa tiêu thụ token không xuất hiện trong PerAgent.
func Test_UsageTracker_PerAgentSkipsZero(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	// Dựng một role nhưng không tiêu thụ token (trường hợp cực đoan)
	tk.perAgent["ghost"] = &agentTotals{}
	tk.Record("writer", "", makeUsageMsg(100, 50, 0, 20))

	per := tk.PerAgent()
	if len(per) != 1 || per[0].Role != "writer" {
		t.Fatalf("ghost role with zero tokens must be skipped, got %+v", per)
	}
}

// Test_UsageTracker_MissingAssistantUsageCounted kiểm chứng missingAssistantUsage
// ranh giới xét của bộ đếm:
//   - đường tích lũy chỉ nhìn Usage != nil (không cứng nhắc Role)
//   - đường chẩn đoán yêu cầu Role=Assistant và Content khác rỗng — mới giống "một lần response LLM thật mà
//     không lấy được usage", tương ứng upstream streaming không gửi final chunk include_usage của OpenAI
//     đó. Các tình huống khác (tin nhắn user/tool, assistant content rỗng)
//     đều không tính là missing.
func Test_UsageTracker_MissingAssistantUsageCounted(t *testing.T) {
	tk := NewUsageTracker(nil, nil)

	withContent := func(text string) agentcore.Message {
		return agentcore.Message{
			Role:    agentcore.RoleAssistant,
			Content: []agentcore.ContentBlock{agentcore.TextBlock(text)},
		}
	}

	// assistant + có Content + nil Usage → trông như response thật nhưng thiếu usage, tính vào chẩn đoán
	tk.Record("writer", "", withContent("hi"))
	tk.Record("writer", "", withContent("again"))
	// assistant nhưng Content rỗng → đường khôi phục bất thường hoặc tin nhắn chỗ đứng, không tính missing
	tk.Record("writer", "", agentcore.Message{Role: agentcore.RoleAssistant})
	// tin nhắn user/tool đương nhiên không mang usage, kể cả Content rỗng hay không đều không tính missing
	tk.Record("writer", "", agentcore.Message{Role: agentcore.RoleUser, Content: []agentcore.ContentBlock{agentcore.TextBlock("u")}})
	tk.Record("writer", "", agentcore.Message{Role: agentcore.RoleTool, Content: []agentcore.ContentBlock{agentcore.TextBlock("t")}})
	// Bình thường có usage → đi đường tích lũy, không tính vào chẩn đoán
	tk.Record("writer", "", makeUsageMsg(100, 50, 0, 20))

	if got := tk.MissingAssistantUsage(); got != 2 {
		t.Errorf("MissingAssistantUsage=%d, want 2", got)
	}
	_, in, _, _, _ := tk.Totals()
	if in != 100 {
		t.Errorf("Đường bình thường tích lũy bị hỏng, input=%d want 100", in)
	}
}

// Test_UsageTracker_CacheCapableFromFacts kiểm chứng khi registry tra không ra model đó thì CacheCapable
// vẫn đánh dấu true được theo "dữ kiện": model của backend tự dựng / proxy nội địa thường không có trong index
// giá của BerriAI/litellm, resolveCost trả capable=false; nhưng chỉ cần backend thật sự trả về
// CacheRead hoặc CacheWrite > 0, là chứng minh model đó khách quan hỗ trợ prompt cache, dòng per-role
// không nên hiển thị "chưa bật".
func Test_UsageTracker_CacheCapableFromFacts(t *testing.T) {
	tk := NewUsageTracker(nil, nil) // modelSet=nil → resolveCost luôn capable=false

	// Một lần có CacheWrite (mô phỏng lần đầu ghi cache, registry không đánh dấu capable, nhưng dữ kiện chứng minh hỗ trợ)
	tk.Record("writer", "", makeUsageMsg(1000, 0, 200, 100))
	per := tk.PerAgent()
	if len(per) != 1 || !per[0].CacheCapable {
		t.Fatalf("CacheWrite > 0 phải đánh dấu ngay CacheCapable=true, got %+v", per)
	}
	if !tk.OverallCacheCapable() {
		t.Errorf("overall CacheCapable cũng phải đồng bộ đặt true")
	}

	// Ngược lại: role hoàn toàn không có hoạt động cache, CacheCapable phải giữ false
	tk.Record("editor", "", makeUsageMsg(500, 0, 0, 100))
	per = tk.PerAgent()
	for _, a := range per {
		if a.Role == "editor" && a.CacheCapable {
			t.Errorf("editor toàn trình không có CacheRead/Write, CacheCapable không nên bị đánh dấu nhầm thành true")
		}
	}
}

// Test_UsageTracker_AccumulatesAnyRoleWithUsage kiểm chứng đường tích lũy tách rời khỏi Role:
// kể cả sau này adapter nào đó lắp usage lên message của vai trò không phải assistant,
// vẫn tích lũy đúng. Giữ hợp đồng "luật lắp ráp và luật tích lũy tách rời".
func Test_UsageTracker_AccumulatesAnyRoleWithUsage(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	// Dựng một tin nhắn không phải assistant mang Usage, về lý thuyết không phổ biến
	hypothetical := agentcore.Message{
		Role:  agentcore.RoleSystem,
		Usage: &agentcore.Usage{Input: 200, Output: 50, CacheRead: 100},
	}
	tk.Record("writer", "", hypothetical)

	_, in, out, cr, _ := tk.Totals()
	if in != 200 || out != 50 || cr != 100 {
		t.Errorf("Không tích lũy theo trường Usage, got (in=%d out=%d cr=%d) want (200 50 100)", in, out, cr)
	}
	if tk.MissingAssistantUsage() != 0 {
		t.Errorf("Có Usage thì không được tính vào missing")
	}
}

// Test_UsageTracker_OnCostCallback kiểm chứng điểm nối dây của sentinel ngân sách: sau mỗi lần ghi sổ
// callback ngoài khóa mang chi phí tích lũy mới nhất (kể cả đường provider tự báo cost).
func Test_UsageTracker_OnCostCallback(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	var got []float64
	tk.SetOnCost(func(total float64) { got = append(got, total) })

	msg := func(cost float64) agentcore.AgentMessage {
		return agentcore.Message{
			Role:  agentcore.RoleAssistant,
			Usage: &agentcore.Usage{Input: 100, Output: 10, Cost: &agentcore.Cost{Total: cost}},
		}
	}
	tk.Record("writer", "", msg(0.5))
	tk.Record("writer", "", msg(0.25))

	if len(got) != 2 || got[0] != 0.5 || got[1] != 0.75 {
		t.Fatalf("onCost should carry growing totals, got %v", got)
	}
}

// Test_UsageTracker_OnMissingUsageOnce kiểm chứng callback vùng mù chỉ kích hoạt lần đầu.
func Test_UsageTracker_OnMissingUsageOnce(t *testing.T) {
	tk := NewUsageTracker(nil, nil)
	fired := 0
	tk.SetOnMissingUsage(func() { fired++ })

	noUsage := agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock("正文")}}
	tk.Record("writer", "", noUsage)
	tk.Record("writer", "", noUsage)
	tk.Record("editor", "", noUsage)

	if fired != 1 {
		t.Fatalf("onMissingUsage should fire exactly once, got %d", fired)
	}
}

// TestCacheBreakDetection kiểm chứng bốn hướng đi của phát hiện đứt gãy chuỗi cache:
// cùng phiên tiền tố tăng + hit giảm mạnh → đứt gãy; đổi task (spawn mới) → đổi cơ sở không so sánh;
// tiền tố ngắn lại (nén trong phiên) → chỉ reset cơ sở không cảnh báo; mức giảm không đạt hai ngưỡng (tương đối 5%
// và tuyệt đối 2000) → không cảnh báo.
func TestCacheBreakDetection(t *testing.T) {
	tk := NewUsageTracker(nil, nil)

	// Dựng cơ sở: tiền tố 30k, hit 28k.
	tk.Record("writer", "写第1章", makeUsageMsg(30000, 28000, 0, 100))
	if got := tk.OverallCacheBreaks(); got != 0 {
		t.Fatalf("Tin nhắn đầu không được xét đứt gãy, got %d", got)
	}

	// Trong cùng phiên tiền tố tăng mà hit giảm mạnh (28k→4k) → đứt gãy.
	tk.Record("writer", "写第1章", makeUsageMsg(34000, 4096, 0, 100))
	if got := tk.OverallCacheBreaks(); got != 1 {
		t.Fatalf("Tiền tố tăng + hit giảm mạnh phải xét 1 lần đứt gãy, got %d", got)
	}

	// Trong cùng phiên tiền tố ngắn lại (nén ngữ cảnh, 4.4k < 34k) → reset cơ sở, không cảnh báo.
	tk.Record("writer", "写第1章", makeUsageMsg(4400, 0, 0, 100))
	if got := tk.OverallCacheBreaks(); got != 1 {
		t.Fatalf("Tiền tố ngắn lại phải coi là nén rồi reset, got %d", got)
	}

	// Trên cơ sở mới giảm nhẹ (mức giảm < ngưỡng tuyệt đối 2000) → không cảnh báo.
	tk.Record("writer", "写第1章", makeUsageMsg(36000, 30000, 0, 100))
	tk.Record("writer", "写第1章", makeUsageMsg(38000, 28500, 0, 100))
	if got := tk.OverallCacheBreaks(); got != 1 {
		t.Fatalf("Mức giảm 1.5k chưa qua ngưỡng tuyệt đối không được cảnh báo, got %d", got)
	}

	// Đổi task = spawn mới = dòng dõi cache mới: kể cả tiền tố lần gọi đầu không ngắn hơn lần cuối phiên trước
	// (38k → 40k) và hit giảm mạnh (28.5k→0), cũng không so sánh không cảnh báo. Đây là hồi quy
	// "báo nhầm phiên ngắn liên tiếp": chiều dò phải khớp mức hạt phiên của prompt_cache_key.
	tk.Record("writer", "写第2章", makeUsageMsg(40000, 0, 0, 100))
	if got := tk.OverallCacheBreaks(); got != 1 {
		t.Fatalf("Đổi task đổi cơ sở, xuyên phiên không được so sánh, got %d", got)
	}

	// Trong phiên mới lại đứt gãy → cảnh báo bình thường (chứng minh cơ sở mới đã hiệu lực).
	tk.Record("writer", "写第2章", makeUsageMsg(45000, 38000, 0, 100))
	tk.Record("writer", "写第2章", makeUsageMsg(48000, 5000, 0, 100))
	if got := tk.OverallCacheBreaks(); got != 2 {
		t.Fatalf("Đứt gãy trong phiên mới phải được dò bình thường, got %d", got)
	}

	// Mức giảm tương đối <5% (100k→96k, giảm 4%) → không cảnh báo (kể cả mức giảm tuyệt đối 4k > 2000).
	tk.Record("editor", "审阅第一弧", makeUsageMsg(120000, 100000, 0, 100))
	tk.Record("editor", "审阅第一弧", makeUsageMsg(125000, 96000, 0, 100))
	if got := tk.OverallCacheBreaks(); got != 2 {
		t.Fatalf("Mức giảm tương đối 4%% chưa qua ngưỡng 5%% không được cảnh báo, got %d", got)
	}

	// Quy về per-role: đứt gãy ghi danh writer và vào Snapshot.
	snap := tk.Snapshot()
	if snap.Overall.CacheBreaks != 2 || snap.PerAgent["writer"].CacheBreaks != 2 {
		t.Fatalf("Bộ đếm đứt gãy phải vào snapshot: overall=%d writer=%d", snap.Overall.CacheBreaks, snap.PerAgent["writer"].CacheBreaks)
	}
}
