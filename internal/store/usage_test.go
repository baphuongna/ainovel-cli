package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// TestUsageStore_LoadMissing xác minh khi tệp không tồn tại thì trả về (nil, nil), bên gọi sẽ đi replay.
func TestUsageStore_LoadMissing(t *testing.T) {
	dir := t.TempDir()
	us := NewUsageStore(newIO(dir))

	state, err := us.Load()
	if err != nil {
		t.Fatalf("Load missing file should not error, got %v", err)
	}
	if state != nil {
		t.Fatalf("Load missing file should return nil state, got %+v", state)
	}
}

// TestUsageStore_RoundTrip ghi rồi đọc lại, xác minh dữ liệu tích lũy quay về nguyên trạng.
func TestUsageStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	us := NewUsageStore(newIO(dir))

	in := domain.UsageState{
		Overall: domain.AgentUsageTotals{
			Input: 12000, Output: 3400, CacheRead: 8000, CacheWrite: 1500,
			Cost: 1.234, Saved: 0.5, CacheCapable: true,
		},
		PerAgent: map[string]domain.AgentUsageTotals{
			"writer": {Input: 10000, Output: 3000, CacheRead: 7500, Cost: 1.0, CacheCapable: true},
			"editor": {Input: 2000, Output: 400, CacheRead: 500, Cost: 0.234},
		},
		MissingUsage: 3,
	}
	if err := us.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := us.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil {
		t.Fatalf("Load returned nil after Save")
	}
	if got.Schema != domain.UsageSchemaVersion {
		t.Errorf("schema = %d want %d", got.Schema, domain.UsageSchemaVersion)
	}
	if got.Overall != in.Overall {
		t.Errorf("overall mismatch:\n got  %+v\n want %+v", got.Overall, in.Overall)
	}
	if got.PerAgent["writer"] != in.PerAgent["writer"] {
		t.Errorf("writer totals mismatch:\n got  %+v\n want %+v", got.PerAgent["writer"], in.PerAgent["writer"])
	}
	if got.MissingUsage != in.MissingUsage {
		t.Errorf("missing_usage = %d want %d", got.MissingUsage, in.MissingUsage)
	}
}

// TestUsageStore_LoadSchemaMismatch xác minh khi nâng cấp schema trong tương lai thì tệp cũ bị vứt
// (để host đi replay dựng lại), không nhét ngược trường không tương thích vào tracker.
func TestUsageStore_LoadSchemaMismatch(t *testing.T) {
	dir := t.TempDir()
	us := NewUsageStore(newIO(dir))

	// Tự viết tay một bản dữ liệu cũ schema=0
	raw, err := json.Marshal(map[string]any{
		"schema":  0,
		"overall": map[string]any{"input": 999},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "meta"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta", "usage.json"), raw, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := us.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != nil {
		t.Errorf("schema mismatch should return nil, got %+v", got)
	}
}
