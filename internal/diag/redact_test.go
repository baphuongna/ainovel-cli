package diag

import (
	"strings"
	"testing"

	"github.com/voocel/agentcore"
)

// ── review F5：错误串与结构 token 的凭据兜底打码 ──

func TestScrubSecrets(t *testing.T) {
	cases := []struct{ in, mustNotContain string }{
		{"GET https://generativelanguage.googleapis.com/?key=AIzaSyD-EXAMPLE123456: 403", "AIzaSyD-EXAMPLE123456"},
		{"401 Unauthorized: Bearer eyJhbGciOi.example.token.value", "eyJhbGciOi.example.token.value"},
		{"openai: sk-proj-4abcd5678EFGH quota exceeded", "sk-proj-4abcd5678EFGH"},
		{"provider error: api_key=\"zh-3xample999key888value\"", "zh-3xample999key888value"},
	}
	for _, c := range cases {
		got := scrubSecrets(c.in)
		if strings.Contains(got, c.mustNotContain) {
			t.Errorf("scrubSecrets(%q) = %q，仍泄漏 %q", c.in, got, c.mustNotContain)
		}
	}
	// 无凭据的错误串不应被改动。
	if got := scrubSecrets("tool argument validation failed: chapter"); got != "tool argument validation failed: chapter" {
		t.Errorf("普通错误串不应被误杀，得到 %q", got)
	}
}

// ErrClass 保留首行，但内嵌凭据必须打码。
func TestRedactMessage_ErrClassScrubbed(t *testing.T) {
	m := agentcore.Message{
		Role: agentcore.RoleTool,
		Content: []agentcore.ContentBlock{{
			Type: agentcore.ContentText,
			Text: "request failed: https://api.example.com/v1?key=AIzaSyEXAMPLE99824ff\nsecond line",
		}},
		Metadata: map[string]any{"is_error": true},
	}
	ev := redactMessage("writer", m)
	if ev.ErrClass == "" || strings.Contains(ev.ErrClass, "AIzaSyEXAMPLE99824ff") {
		t.Fatalf("ErrClass 应保留首行但打码凭据，得到 %q", ev.ErrClass)
	}
	if !strings.Contains(ev.ErrClass, "request failed") {
		t.Errorf("ErrClass 应保留错误语境，得到 %q", ev.ErrClass)
	}
}

// 形似凭据的短 token 不应以结构信号名义留在工具参数里。
func TestProjectValue_SecretTokenRedacted(t *testing.T) {
	got := projectValue([]byte(`"sk-abc123xyz456"`))
	if strings.Contains(got, "sk-abc123xyz456") {
		t.Fatalf("sk- token 应被打码，得到 %q", got)
	}
	if !strings.Contains(got, "<redacted") {
		t.Fatalf("应替换为 redacted 占位，得到 %q", got)
	}
	// 普通枚举值不受影响。
	if got := projectValue([]byte(`"premise"`)); got != `"premise"` {
		t.Errorf("普通枚举值应保留，得到 %q", got)
	}
}

// URL 编码形态（%3Fkey%3D…）与裸 AIza 前缀 key（review V-2 MINOR5）。
func TestScrubSecretsEncodedAndAIza(t *testing.T) {
	cases := []struct {
		in            string
		mustNotAppear string
	}{
		{"GET %3Fkey%3DAIzaSyABCDEF1234567890 failed", "AIzaSyABCDEF1234567890"},
		{"provider key AIzaSyABCDEF1234567890 rejected", "AIzaSyABCDEF1234567890"},
	}
	for _, c := range cases {
		if got := scrubSecrets(c.in); strings.Contains(got, c.mustNotAppear) {
			t.Errorf("scrubSecrets(%q) = %q，仍泄漏 %q", c.in, got, c.mustNotAppear)
		}
	}
	// 普通含 % 的文本不应被解码改写。
	in := "download 100% done, path /a%20b"
	if got := scrubSecrets(in); got != in {
		t.Errorf("无凭据文本不应被改动，得到 %q", got)
	}
}
