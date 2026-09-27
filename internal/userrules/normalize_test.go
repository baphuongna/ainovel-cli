package userrules

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

func TestExtractJSON_StripsCodeFences(t *testing.T) {
	cases := []struct{ in, wantHas string }{
		{"```json\n{\"a\":1}\n```", `"a":1`},
		{"```\n{\"a\":1}\n```", `"a":1`},
		{"前缀解释\n{\"a\":1}\n后缀", `"a":1`},
		{"{\"a\":1}", `"a":1`},
	}
	for _, c := range cases {
		got := llmcontract.ExtractJSONObject(c.in)
		if got == "" {
			t.Fatalf("extractJSON(%q) trả về rỗng", c.in)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(got), &m); err != nil {
			t.Fatalf("extractJSON(%q)=%q không phải JSON hợp lệ: %v", c.in, got, err)
		}
	}
	if llmcontract.ExtractJSONObject("Không có JSON nào cả") != "" {
		t.Fatal("Không có JSON thì phải trả về chuỗi rỗng")
	}
}

func TestParseNormalizerJSON_FullOutput(t *testing.T) {
	raw := "```json\n" + `{
  "structured": {
    "genre": "都市",
    "forbidden_chars": [],
    "forbidden_phrases": ["某种程度上"],
    "fatigue_words": [{"word": "竟然", "max_per_chapter": 2}]
  },
  "preferences": "主角冷静克制",
  "uncertain": ["少用比喻：无阈值"]
}` + "\n```"
	body := llmcontract.ExtractJSONObject(raw)
	if err := llmcontract.ValidateJSON(normalizeContract.Schema, []byte(body)); err != nil {
		t.Fatalf("Phải parse thành công: %v", err)
	}
	var out normalizerOutput
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("Phải decode thành công: %v", err)
	}
	cand, err := out.toCandidate("startup_prompt")
	if err != nil {
		t.Fatalf("toCandidate: %v", err)
	}
	if cand.Structured.Genre != "都市" {
		t.Fatalf("genre parse sai: %+v", cand.Structured)
	}
	if len(cand.Structured.ForbiddenPhrases) != 1 || cand.Structured.ForbiddenPhrases[0] != "某种程度上" {
		t.Fatalf("forbidden_phrases parse sai: %v", cand.Structured.ForbiddenPhrases)
	}
	if cand.Structured.FatigueWords["竟然"] != 2 {
		t.Fatalf("mảng fatigue_words phải chuyển thành map: %v", cand.Structured.FatigueWords)
	}
	if cand.Preferences != "主角冷静克制" {
		t.Fatalf("preferences parse sai: %q", cand.Preferences)
	}
	if len(cand.Uncertain) != 1 {
		t.Fatalf("uncertain phải có 1 mục, nhận %v", cand.Uncertain)
	}
}

// Kiểm tra mục fatigue: từ rỗng và ngưỡng không nguyên dương đều là lỗi nghiệp vụ có thể phản hồi để sửa.
func TestToCandidateRejectsInvalidFatigueEntries(t *testing.T) {
	bad := normalizerOutput{Structured: normalizerStructured{
		FatigueWords: []fatigueWordEntry{{Word: " ", MaxPerChapter: 2}},
	}}
	if _, err := bad.toCandidate("x"); err == nil {
		t.Fatal("Mục từ rỗng phải báo lỗi")
	}
	bad = normalizerOutput{Structured: normalizerStructured{
		FatigueWords: []fatigueWordEntry{{Word: "竟然", MaxPerChapter: 0}},
	}}
	if _, err := bad.toCandidate("x"); err == nil {
		t.Fatal("Ngưỡng không nguyên dương phải báo lỗi")
	}
}

func TestParseNormalizerJSON_GarbageFails(t *testing.T) {
	if body := llmcontract.ExtractJSONObject("Model chỉ trả một câu, không có JSON"); body != "" {
		t.Fatal("Không có JSON phải parse thất bại (kích hoạt hạ cấp)")
	}
	if body := llmcontract.ExtractJSONObject("{ 不完整"); body != "" {
		t.Fatal("JSON cụt phải parse thất bại")
	}
}

// Kiểm tra contract (RFC §11.1): gốc là object, mọi thuộc tính (kể cả structured/fatigue_words lồng nhau) đều required.
func TestNormalizeContractIsStrictReady(t *testing.T) {
	if normalizeContract.Schema["type"] != "object" {
		t.Fatal("Gốc phải là object")
	}
	if err := llmcontract.ValidateStrictReady(normalizeContract.Schema); err != nil {
		t.Fatal(err)
	}
}

func TestNormalize_NilModelErrors(t *testing.T) {
	// Không có model dùng được: trả lỗi rõ ràng, tầng Service hạ cấp thành raw preferences.
	var n *Normalizer = NewNormalizer(nil)
	if _, err := n.Normalize(t.Context(), "startup_prompt", "每章1200字，主角冷静"); err == nil {
		t.Fatal("Không có model phải trả lỗi")
	}
}

// scriptedModel là fake ChatModel tối thiểu: nhả câu trả lời định sẵn theo thứ tự gọi, đồng thời
// ghi lại messages của vòng cuối, để assert việc thử lại phản hồi có ghép gợi ý sửa vào vòng
// đối thoại sau hay không. Hết câu trả lời thì lặp lại câu cuối.
type scriptedModel struct {
	replies  []string
	calls    int
	lastMsgs []agentcore.Message
	lastCfg  agentcore.CallConfig
	err      error // khác nil thì Generate luôn trả lỗi đó
	cancel   context.CancelFunc
	cancelAt int
}

func (m *scriptedModel) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	var cfg agentcore.CallConfig
	for _, o := range opts {
		o(&cfg)
	}
	m.lastCfg = cfg
	m.lastMsgs = messages
	m.calls++
	if m.cancel != nil && m.cancelAt > 0 && m.calls >= m.cancelAt {
		m.cancel()
	}
	if m.err != nil {
		return nil, m.err
	}
	i := m.calls - 1
	if i >= len(m.replies) {
		i = len(m.replies) - 1
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{
		Role:    agentcore.RoleAssistant,
		Content: []agentcore.ContentBlock{agentcore.TextBlock(m.replies[i])},
	}}, nil
}

func (m *scriptedModel) GenerateStream(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	return nil, nil
}

func (m *scriptedModel) SupportsTools() bool { return false }

// Thử lại phản hồi: vòng đầu nhả JSON hỏng, vòng sau mới hợp lệ. Normalize phải thành công,
// và hội thoại vòng sau có mang theo đầu ra hỏng và gợi ý sửa của vòng trước (phản hồi,
// chứ không phải thử lại mù nguyên văn).
func TestNormalize_FeedbackRetryRecovers(t *testing.T) {
	model := &scriptedModel{replies: []string{
		"这不是 JSON",
		`{"structured":{"genre":"","forbidden_chars":[],"forbidden_phrases":["某种程度上"],"fatigue_words":[]},"preferences":"","uncertain":[]}`,
	}}
	n := NewNormalizer(model)

	cand, err := n.Normalize(t.Context(), "startup_prompt", "不要出现某种程度上")
	if err != nil {
		t.Fatalf("Vòng sau đã trả JSON hợp lệ, không được thất bại: %v", err)
	}
	if len(cand.Structured.ForbiddenPhrases) != 1 {
		t.Fatalf("Phải parse ra forbidden_phrases, got %+v", cand.Structured)
	}
	if model.calls != 2 {
		t.Fatalf("Phải thành công ở lần thứ 2, thực tế gọi %d lần", model.calls)
	}

	var sawBad, sawHint bool
	for _, msg := range model.lastMsgs {
		text := msg.TextContent()
		if text == "这不是 JSON" {
			sawBad = true
		}
		if strings.Contains(text, "JSON Schema") && strings.Contains(text, "Lỗi: ") {
			sawHint = true
		}
	}
	if !sawBad || !sawHint {
		t.Errorf("Vòng sau phải ghép đầu ra hỏng vòng trước và gợi ý sửa, sawBad=%v sawHint=%v", sawBad, sawHint)
	}
	system := model.lastMsgs[0].TextContent()
	if !strings.Contains(system, "<output-json-schema>") || !strings.Contains(system, `"fatigue_words"`) {
		t.Fatalf("Contract phải tự đính kèm schema từ Contract:\n%s", system)
	}
}

// Chuẩn hóa không đè thinking mặc định của model; model chat thường sẽ từ chối tắt tường minh.
func TestNormalize_LeavesThinkingUnspecifiedAndReservesTokens(t *testing.T) {
	model := &scriptedModel{replies: []string{`{"structured":{"genre":"","forbidden_chars":[],"forbidden_phrases":[],"fatigue_words":[]},"preferences":"x","uncertain":[]}`}}
	n := NewNormalizer(model)

	if _, err := n.Normalize(t.Context(), "startup_prompt", "随便一条规则"); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if model.lastCfg.ThinkingLevel != agentcore.ThinkingAuto {
		t.Errorf("Không nên gửi tham số thinking, got %q", model.lastCfg.ThinkingLevel)
	}
	if model.lastCfg.MaxTokens != normalizeMaxTokens {
		t.Errorf("max_tokens phải là %d, got %d", normalizeMaxTokens, model.lastCfg.MaxTokens)
	}
}

// JSON hỏng suốt quá trình: không có giới hạn số lần cố định, liên tục phản hồi hỏi lại,
// cho đến khi context bị hủy.
func TestNormalize_FeedbackRetryContinuesUntilContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	model := &scriptedModel{replies: []string{"坏"}, cancel: cancel, cancelAt: 4}
	n := NewNormalizer(model)

	_, err := n.Normalize(ctx, "startup_prompt", "每章1200字")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Phải do context chấm dứt vòng tự chữa, nhận %v", err)
	}
	if model.calls != 4 {
		t.Fatalf("Trước khi context hủy phải gọi liên tục, thực tế %d lần", model.calls)
	}
}

type terminalTestError struct{}

func (terminalTestError) Error() string   { return "401 authentication failed" }
func (terminalTestError) Retryable() bool { return false }

type retryableTestError struct{}

func (retryableTestError) Error() string             { return "provider unavailable" }
func (retryableTestError) Retryable() bool           { return true }
func (retryableTestError) RetryAfter() time.Duration { return time.Millisecond }

// Lỗi chấm dứt (401 v.v.) không được thử lại mù: đúng 1 lần gọi là trả lỗi.
func TestNormalize_TerminalErrorStopsImmediately(t *testing.T) {
	model := &scriptedModel{err: terminalTestError{}}
	n := NewNormalizer(model)

	_, err := n.Normalize(t.Context(), "startup_prompt", "规则")
	if err == nil || !errors.As(err, &terminalTestError{}) {
		t.Fatalf("Phải lộ lỗi chấm dứt: %v", err)
	}
	if model.calls != 1 {
		t.Fatalf("Lỗi chấm dứt không được thử lại, thực tế gọi %d lần", model.calls)
	}
}

// Lỗi request retryable được llmretry thử lại với backoff.
type flakyModel struct {
	scriptedModel
	failures int
}

func (m *flakyModel) Generate(ctx context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	if m.scriptedModel.calls < m.failures {
		m.scriptedModel.calls++
		return nil, retryableTestError{}
	}
	return m.scriptedModel.Generate(ctx, msgs, tools, opts...)
}

func TestNormalize_RetryableErrorRecovers(t *testing.T) {
	model := &flakyModel{
		scriptedModel: scriptedModel{replies: []string{`{"structured":{"genre":"","forbidden_chars":[],"forbidden_phrases":[],"fatigue_words":[]},"preferences":"x","uncertain":[]}`}},
		failures:      2,
	}
	n := NewNormalizer(model)
	cand, err := n.Normalize(t.Context(), "startup_prompt", "规则")
	if err != nil || cand.Preferences != "x" {
		t.Fatalf("Sau backoff phải thành công: %+v %v", cand, err)
	}
}

// nativeRulesModel khai báo hỗ trợ JSON Schema native.
type nativeRulesModel struct {
	*scriptedModel
}

func (m *nativeRulesModel) Capabilities() llm.Capabilities {
	return llm.Capabilities{
		Provider:   "openai",
		Model:      "gpt-test",
		Structured: llm.StructuredCapabilities{JSONSchema: llm.SupportYes, Strict: llm.SupportYes},
	}
}

func TestNormalize_NativeSendsSchemaAndRejectsFences(t *testing.T) {
	// Chế độ native: schema vào request; JSON trần thành công.
	model := &nativeRulesModel{&scriptedModel{replies: []string{
		`{"structured":{"genre":"","forbidden_chars":[],"forbidden_phrases":[],"fatigue_words":[]},"preferences":"x","uncertain":[]}`,
	}}}
	n := NewNormalizer(model)
	cand, err := n.Normalize(t.Context(), "startup_prompt", "规则")
	if err != nil || cand.Preferences != "x" {
		t.Fatalf("Chuẩn hóa native thất bại: %+v %v", cand, err)
	}
	rf := model.lastCfg.ResponseFormat
	if rf == nil || rf.JSONSchema == nil || rf.JSONSchema.Name != "userrules_normalize" {
		t.Fatalf("Chế độ native phải gửi schema: %+v", rf)
	}
	if got := model.lastMsgs[0].TextContent(); got != normalizerSystemPrompt {
		t.Fatalf("Chế độ native không được inject schema vào prompt lần nữa:\n%s", got)
	}

	// Đầu ra có fence = vi phạm contract: báo lỗi ngay, không đi extractJSON, không hỏi lại.
	fenced := &nativeRulesModel{&scriptedModel{replies: []string{
		"```json\n{\"structured\":{},\"preferences\":\"x\",\"uncertain\":[]}\n```",
	}}}
	n = NewNormalizer(fenced)
	_, err = n.Normalize(t.Context(), "startup_prompt", "规则")
	if err == nil || !strings.Contains(err.Error(), "vi phạm hợp đồng") {
		t.Fatalf("Mong đợi lỗi vi phạm contract, got %v", err)
	}
	if fenced.calls != 1 {
		t.Fatalf("Vi phạm contract không được hỏi lại, thực tế %d lần", fenced.calls)
	}
}
