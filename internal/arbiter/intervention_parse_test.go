package arbiter

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// ── review 测试缺口：DecideIntervention 的解析-校验-重试闭环 ──

func writingFacts() InterventionFacts {
	return InterventionFacts{
		Phase:             string(domain.PhaseWriting),
		CompletedChapters: 10,
		NextChapter:       11,
	}
}

// 首轮输出是坏 JSON（markdown 围栏包裹），经反馈修正后第二轮给出合法决策。
func TestDecideInterventionRecoversFromMalformedOutput(t *testing.T) {
	m := &scriptedModel{outputs: []string{
		"```json\n{\"answer\":\"围栏包裹的坏输出\",\"reason\":\"x\"}\n```",
		`{"answer":"好的，已了解您的意见","rules":null,"hold":null,"reopen":null,"dispatch":null,"reason":"用户表达的是意见而非指令"}`,
	}}
	d, err := DecideIntervention(t.Context(), m, "sys", writingFacts(), "我觉得主角可以更果断一点")
	if err != nil {
		t.Fatalf("第二轮合法输出应被接受: %v", err)
	}
	if d.Answer == "" || d.Reason == "" {
		t.Fatalf("决策解码错误: %+v", d)
	}
}

// 校验失败（空决策）触发反馈重试，随后收敛到合法动作。
func TestDecideInterventionRejectsEmptyDecisionThenRecovers(t *testing.T) {
	m := &scriptedModel{outputs: []string{
		`{"answer":"","rules":null,"hold":null,"reopen":null,"dispatch":null,"reason":"空决策"}`,
		`{"answer":"已把\"多用动作场面\"写入本书规则","rules":"多用动作场面","hold":null,"reopen":null,"dispatch":null,"reason":"用户提出风格偏好"}`,
	}}
	d, err := DecideIntervention(t.Context(), m, "sys", writingFacts(), "以后多用动作场面")
	if err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}
	if d.Rules == "" {
		t.Fatalf("rules 动作应解码: %+v", d)
	}
}

// dispatch.agent 不在白名单 → 校验拒绝；第二轮改派合法 worker。
func TestDecideInterventionRejectsIllegalDispatchAgent(t *testing.T) {
	m := &scriptedModel{outputs: []string{
		`{"answer":"","rules":null,"hold":null,"reopen":null,"dispatch":{"agent":"evil_worker","task":"干坏事"},"reason":"x"}`,
		`{"answer":"","rules":null,"hold":null,"reopen":null,"dispatch":{"agent":"editor","task":"全文润色一遍"},"reason":"用户要求润色"}`,
	}}
	d, err := DecideIntervention(t.Context(), m, "sys", writingFacts(), "帮我全文润色")
	if err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}
	if d.Dispatch == nil || d.Dispatch.Agent != "editor" {
		t.Fatalf("dispatch 解码错误: %+v", d.Dispatch)
	}
}

// 规划期派发 writer 属于阶段违规，必须被 ValidateAgainst 拒绝。
func TestDecideInterventionRejectsWriterInPlanningPhase(t *testing.T) {
	m := &scriptedModel{outputs: []string{
		`{"answer":"","rules":null,"hold":null,"reopen":null,"dispatch":{"agent":"writer","task":"直接写第一章"},"reason":"x"}`,
		`{"answer":"规划尚未完成，先让架构师补全大纲","rules":null,"hold":null,"reopen":null,"dispatch":{"agent":"architect_long","task":"补全三层大纲"},"reason":"规划期只能派架构师"}`,
	}}
	d, err := DecideIntervention(t.Context(), m, "sys", InterventionFacts{
		Phase: string(domain.PhaseOutline), NextChapter: 1,
	}, "直接开写吧")
	if err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}
	if d.Dispatch == nil || !strings.HasPrefix(d.Dispatch.Agent, "architect") {
		t.Fatalf("规划期只应派架构师: %+v", d.Dispatch)
	}
}
