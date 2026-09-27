package agents

// Hợp đồng lắp ráp ContextManager của các Worker (build.go).
//
// Trước đây chỉ Writer có ContextManagerFactory: architect_short/architect_long/editor
// không bao giờ nén context, phiên dài tràn tới trần provider. Nhóm test này đóng cứng
// hợp đồng mới —— mọi agent phải có factory, factory phải resolve window ĐỘNG theo model
// được truyền vào (tái tạo khi /model đổi, không capture tĩnh lúc build), và riêng Writer
// mới được dùng chiến lược store_summary (StoreSummaryCompact nạp narrative summary
// continuity của Writer; Architect/Editor phải giữ cặp rủi ro thấp
// tool_result_microcompact + full_summary để không mất task state).
//
// Cấp độ: integration hermetic —— BuildWorkers thật (store tempdir + assets nhúng +
// ModelSet dựng offline, không gọi mạng), không phụ thuộc thời gian hay thứ tự chạy.
// Mock duy nhất là ChatModel, vốn là tham số chính thức của ContextManagerFactory.

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// Cửa sổ nhỏ để budget nén dễ vượt ngưỡng trong test: window < floor reserve
// (MinCompactReserve=8000) → threshold = 0 → mọi messages đều đủ điều kiện chạy
// chiến lược (xem bootstrap.CompactReserveTokens + ContextEngine.computeBudget).
const (
	tinyTestWindow = 4000
	bigTestWindow  = 16000

	storeSummaryStrategy = "store_summary"            // Writer-only (ctxpack.StoreSummaryCompact)
	microcompactStrategy = "tool_result_microcompact" // mọi agent
	fullSummaryStrategy  = "full_summary"             // mọi agent
)

// fakeRoleModel bọc contractModel (agentcore_contract_test.go) thêm Info() để
// bootstrap.ModelProvider/ModelName trích được provider/name —— đúng đường
// ContextManagerFactory dùng để resolve window.
type fakeRoleModel struct {
	*contractModel
	info llm.ModelInfo
}

func (m *fakeRoleModel) Info() llm.ModelInfo { return m.info }

// roleContextModel tạo mock luôn trả về văn bản kết thúc bình thường;
// FullSummary sẽ gọi nó khi tóm tắt —— ở đây chỉ cần không lỗi.
func roleContextModel(provider, name string) *fakeRoleModel {
	return &fakeRoleModel{
		contractModel: &contractModel{fn: func(int, []agentcore.Message) (*agentcore.LLMResponse, error) {
			return &agentcore.LLMResponse{Message: assistantText("tóm tắt nhiệm vụ hiện tại", agentcore.StopReasonStop)}, nil
		}},
		info: llm.ModelInfo{Provider: provider, Name: name},
	}
}

// buildFixtureConfig dựng Config theo pattern host/model_config_test.go:
// provider openai trỏ localhost (không gọi mạng lúc dựng model), hai model
// tiny/big khai báo context_window khác nhau để kiểm chứng window resolve động.
func buildFixtureConfig() bootstrap.Config {
	return bootstrap.Config{
		Provider:  "proxy",
		ModelName: "tiny",
		Providers: map[string]bootstrap.ProviderConfig{"proxy": {
			Type:    "openai",
			APIKey:  "test",
			BaseURL: "http://127.0.0.1:9/v1",
			Models: []bootstrap.ModelConfig{
				{Name: "tiny", ContextWindow: tinyTestWindow},
				{Name: "big", ContextWindow: bigTestWindow},
			},
		}},
	}
}

// buildWorkerConfigs chạy BuildWorkers thật và lấy subagent.Config của cả 4 agent
// qua Runner.AgentConfig (kênh public, không đụng nội bộ build).
func buildWorkerConfigs(t *testing.T) map[string]subagent.Config {
	t.Helper()
	cfg := buildFixtureConfig()
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("new model set: %v", err)
	}
	st := store.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	bundle := assets.Load("", assets.LoadOptions{})
	runner, _, _ := BuildWorkers(cfg, st, tools.NewStyleStatsIndex(st), models, bundle, nil, nil)

	configs := make(map[string]subagent.Config, 4)
	for _, name := range []string{"architect_short", "architect_long", "writer", "editor"} {
		c, ok := runner.AgentConfig(name)
		if !ok {
			t.Fatalf("agent %q phải được đăng ký trong Runner", name)
		}
		configs[name] = c
	}
	return configs
}

// Duck-typing hai núm public của *corecontext.ContextEngine mà test cần,
// tránh ràng buộc tên kiểu cụ thể từ module ngoài.
type engineWindowReader interface{ ContextWindow() int }
type engineStrategyHookInstaller interface {
	SetStrategyHook(fn func(strategy string) func())
}

// Hợp đồng 1: mọi agent đều có ContextManagerFactory != nil.
// writer là regression guard —— nâng cấp architect/editor không được làm mất
// nén context của Writer.
func TestBuildWorkers_EveryAgentHasContextManagerFactory(t *testing.T) {
	configs := buildWorkerConfigs(t)
	for _, name := range []string{"architect_short", "architect_long", "writer", "editor"} {
		cfg, ok := configs[name]
		if !ok {
			t.Fatalf("thiếu config agent %q", name)
		}
		if cfg.ContextManagerFactory == nil {
			t.Errorf("agent %q phải có ContextManagerFactory (nén context theo context_window)", name)
		}
	}
}

// Hợp đồng 2: factory trả về ContextManager không nil và resolve window ĐỘNG
// theo model được truyền vào —— gọi với tiny phải ra 4000, với big phải ra 16000.
// Nếu factory capture window tĩnh lúc build thì hai lần gọi sẽ ra cùng giá trị.
func TestBuildWorkers_FactoryResolvesWindowPerModel(t *testing.T) {
	configs := buildWorkerConfigs(t)
	for _, name := range []string{"architect_short", "architect_long", "writer", "editor"} {
		t.Run(name, func(t *testing.T) {
			factory := configs[name].ContextManagerFactory
			if factory == nil {
				t.Fatalf("agent %q thiếu ContextManagerFactory", name)
			}
			for _, tc := range []struct {
				model string
				want  int
			}{{"tiny", tinyTestWindow}, {"big", bigTestWindow}} {
				cm := factory(roleContextModel("proxy", tc.model))
				if cm == nil {
					t.Fatalf("factory với model %q trả về ContextManager nil", tc.model)
				}
				reader, ok := cm.(engineWindowReader)
				if !ok {
					t.Fatalf("ContextManager của %q phải là engine có ContextWindow() (got %T)", name, cm)
				}
				if got := reader.ContextWindow(); got != tc.want {
					t.Errorf("window của %q với model %q = %d, muốn %d (factory phải resolve theo model được truyền, không capture tĩnh)", name, tc.model, got, tc.want)
				}
			}
		})
	}
}

// Hợp đồng 3: phân vùng chiến lược nén. Writer (và chỉ Writer) được phép dùng
// store_summary —— nạp narrative continuity từ store. Architect/Editor dùng
// tool_result_microcompact + full_summary, KHÔNG được dùng store_summary
// (nạp narrative summary vô ích + mất task state; docs/context-management.md).
//
// Cách đo: lắp SetStrategyHook rồi Project() một transcript vượt ngưỡng
// (threshold=0 nhờ window nhỏ) —— hook báo tên mọi chiến lược được chạy.
// Writer chứa store_summary là đối chứng dương: chứng minh harness đo được,
// khẳng định phủ định với architect/editor không rỗng nghĩa.
func TestBuildWorkers_StrategyPartitionBetweenWriterAndOthers(t *testing.T) {
	configs := buildWorkerConfigs(t)

	msgs := []agentcore.AgentMessage{
		agentcore.UserMsg(strings.Repeat("hồ sơ nền tảng truyện ", 400)),
		agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock(strings.Repeat("đã ghi nhớ bối cảnh ", 300))}},
		agentcore.UserMsg("tiếp tục nhiệm vụ"),
	}

	strategiesRun := func(t *testing.T, name string) []string {
		t.Helper()
		cm := configs[name].ContextManagerFactory(roleContextModel("proxy", "tiny"))
		if cm == nil {
			t.Fatalf("agent %q thiếu ContextManagerFactory", name)
		}
		installer, ok := cm.(engineStrategyHookInstaller)
		if !ok {
			t.Fatalf("ContextManager của %q phải hỗ trợ SetStrategyHook (got %T)", name, cm)
		}
		var ran []string
		installer.SetStrategyHook(func(strategy string) func() {
			ran = append(ran, strategy)
			return func() {}
		})
		if _, err := cm.Project(context.Background(), msgs); err != nil {
			t.Fatalf("Project của %q: %v", name, err)
		}
		if len(ran) == 0 {
			t.Fatalf("transcript vượt ngưỡng phải kích hoạt ít nhất một chiến lược nén ở %q", name)
		}
		return ran
	}

	t.Run("writer_đối_chứng_dương", func(t *testing.T) {
		ran := strategiesRun(t, "writer")
		hasStore := false
		for _, s := range ran {
			if s == storeSummaryStrategy {
				hasStore = true
			}
		}
		if !hasStore {
			t.Errorf("Writer phải chạy %s (narrative continuity) —— nếu không, khẳng định phủ định với architect/editor mất đối chứng; đã chạy: %v", storeSummaryStrategy, ran)
		}
	})

	for _, name := range []string{"architect_short", "architect_long", "editor"} {
		t.Run(name+"_không_store_summary", func(t *testing.T) {
			ran := strategiesRun(t, name)
			seen := map[string]bool{}
			for _, s := range ran {
				seen[s] = true
				if s == storeSummaryStrategy {
					t.Errorf("%q không được dùng %s (narrative summary là của Writer; sẽ nạp nội dung vô ích và mất task state); đã chạy: %v", name, storeSummaryStrategy, ran)
				}
			}
			if !seen[microcompactStrategy] {
				t.Errorf("%q phải chạy %s (nén tool_result lớn, rủi ro thấp); đã chạy: %v", name, microcompactStrategy, ran)
			}
			if !seen[fullSummaryStrategy] {
				t.Errorf("%q phải chạy %s (checkpoint tóm tắt phiên); đã chạy: %v", name, fullSummaryStrategy, ran)
			}
		})
	}
}
