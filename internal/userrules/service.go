package userrules

import (
	"context"
	"log/slog"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/rules"
	"github.com/voocel/ainovel-cli/internal/store"
)

// Service điều phối việc sinh và cập nhật snapshot quy tắc người dùng: chuẩn hóa từng nguồn
// → hợp nhất tất định → ghi xuống đĩa.
//
// Hai nơi gọi dùng chung một logic:
//   - Mở sách/làm mới: Build / GetOrBuild, do Host gọi tất định.
//   - Cập nhật lúc chạy: Arbiter trích rules xong, Host gọi AddRuntimeRule.
type Service struct {
	store     *store.Store
	norm      *Normalizer
	rulesOpts rules.LoadOptions
}

// NewService dựng service. model dùng để chuẩn hóa (nên là model năng lực mạnh); model nil thì
// mọi nguồn hạ cấp thành raw preferences (vẫn xuất được snapshot, kiểm tra cơ học do system_defaults buộc phải).
func NewService(st *store.Store, model agentcore.ChatModel, opts rules.LoadOptions) *Service {
	return &Service{store: st, norm: NewNormalizer(model), rulesOpts: opts}
}

// normalizeOrDegrade chuẩn hóa một nguồn; thất bại thì ghi lỗi thật và hạ cấp thành raw preferences
// (snapshot Status=degraded, giữ nguyên văn bản gốc) — hạ cấp là sự thật thấy được, nguyên nhân lỗi vào log.
func (s *Service) normalizeOrDegrade(ctx context.Context, source, text string) rules.Candidate {
	cand, err := s.norm.Normalize(ctx, source, text)
	if err != nil {
		slog.Warn("Chuẩn hóa quy tắc thất bại, hạ cấp thành sở thích từ văn bản gốc", "module", "rules", "source", source, "err", err)
		return degraded(source, text)
	}
	return cand
}

// Build sinh snapshot từ các nguồn tĩnh (system_defaults + file rules + prompt khởi động) bằng
// chuẩn hóa và ghi xuống đĩa. Gọi khi mở sách/làm mới. startupPrompt có thể rỗng.
func (s *Service) Build(ctx context.Context, startupPrompt string) (*rules.Snapshot, error) {
	cands := []rules.Candidate{rules.SystemDefaults()}
	for _, rs := range rules.RawFileSources(s.rulesOpts) {
		cands = append(cands, s.normalizeOrDegrade(ctx, rs.Label, rs.Text))
	}
	if strings.TrimSpace(startupPrompt) != "" {
		cands = append(cands, s.normalizeOrDegrade(ctx, "startup_prompt", startupPrompt))
	}
	snap := rules.BuildSnapshot(cands)
	if err := s.store.UserRules.Save(&snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// GetOrBuild trả về snapshot hiện tại; thiếu thì khởi tạo theo system_defaults + file rules.
// Đường đọc lúc runtime đều đi qua đây.
func (s *Service) GetOrBuild(ctx context.Context) (*rules.Snapshot, error) {
	cur, err := s.store.UserRules.Load()
	if err != nil {
		return nil, err
	}
	if cur != nil {
		return cur, nil
	}
	return s.Build(ctx, "")
}

// AddRuntimeRule chuẩn hóa một quy tắc dài hạn lúc chạy, phủ lên snapshot hiện tại với mức
// ưu tiên cao nhất và ghi xuống đĩa. Không bao giờ báo lỗi vì chuẩn hóa thất bại — thất bại thì
// mục đó hạ cấp thành raw preferences. Trả về snapshot sau khi phủ và ứng viên chuẩn hóa lần này.
func (s *Service) AddRuntimeRule(ctx context.Context, text string) (*rules.Snapshot, rules.Candidate, error) {
	cur, err := s.GetOrBuild(ctx)
	if err != nil {
		return nil, rules.Candidate{}, err
	}
	cand := s.normalizeOrDegrade(ctx, "runtime_update", text)
	merged := rules.OverlaySnapshot(*cur, cand)
	if err := s.store.UserRules.Save(&merged); err != nil {
		return nil, cand, err
	}
	return &merged, cand, nil
}
