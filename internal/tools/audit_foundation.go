package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/ainovel-cli/internal/store"
)

// AuditFoundationTool tiếp nhận kết luận xem xét ngữ nghĩa của Architect đối với thiết lập nền tảng đã ghi đĩa.
// Ngữ nghĩa văn học và xuyên-file do model phán đoán; công cụ chỉ bảo đảm nhất quán về phiên bản xem xét, kết luận và chuyển đổi trạng thái.
type AuditFoundationTool struct {
	store *store.Store
}

func NewAuditFoundationTool(store *store.Store) *AuditFoundationTool {
	return &AuditFoundationTool{store: store}
}

func (t *AuditFoundationTool) Name() string { return "audit_foundation" }
func (t *AuditFoundationTool) Description() string {
	return "Xem xét xem book, premise, outline, characters, world_rules và compass đã ghi đĩa có nhất quán ngữ nghĩa hay không." +
		"Phải gọi lại novel_context trước, và truyền nguyên văn foundation_status.fingerprint."
}
func (t *AuditFoundationTool) Label() string                          { return "Xem xét thiết lập" }
func (t *AuditFoundationTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *AuditFoundationTool) ConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *AuditFoundationTool) StrictSchema() bool                     { return true }

func (t *AuditFoundationTool) Schema() map[string]any {
	issue := schema.Object(
		schema.Property("artifact", schema.String("artefact có vấn đề, ví dụ book/premise/characters/layered_outline/world_rules/compass")).Required(),
		schema.Property("description", schema.String("vấn đề ngữ nghĩa xuyên-file")).Required(),
		schema.Property("evidence", schema.String("bằng chứng xung đột cụ thể lấy từ nội dung đã ghi đĩa")).Required(),
		schema.Property("suggestion", llmcontract.Nullable(schema.String("hướng sửa đề xuất; là null khi không cần đề xuất"))).Required(),
	)
	return schema.Object(
		schema.Property("fingerprint", schema.String("foundation_status.fingerprint do novel_context trả về")).Required(),
		schema.Property("ready", schema.Bool("mọi thiết lập nền tảng đã nhất quán ngữ nghĩa và có thể bước vào giai đoạn viết hay chưa")).Required(),
		schema.Property("summary", schema.String("tóm tắt kết luận xem xét")).Required(),
		schema.Property("issues", schema.Array("các vấn đề ngữ nghĩa xuyên-file phát hiện được; là mảng rỗng khi ready=true", issue)).Required(),
	)
}

func (t *AuditFoundationTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var audit domain.FoundationAudit
	if err := json.Unmarshal(args, &audit); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if strings.TrimSpace(audit.Fingerprint) == "" {
		return nil, fmt.Errorf("fingerprint is required: %w", errs.ErrToolArgs)
	}
	if strings.TrimSpace(audit.Summary) == "" {
		return nil, fmt.Errorf("summary is required: %w", errs.ErrToolArgs)
	}

	missing, err := t.store.FoundationMissing()
	if err != nil {
		return nil, fmt.Errorf("load foundation state: %w: %w", errs.ErrStoreRead, err)
	}
	for _, item := range missing {
		if item != "foundation_audit" {
			return nil, fmt.Errorf("thiết lập nền tảng còn thiếu %s, không thể xem xét: %w", item, errs.ErrToolPrecondition)
		}
	}
	current, err := t.store.FoundationFingerprint()
	if err != nil {
		return nil, fmt.Errorf("fingerprint foundation: %w: %w", errs.ErrStoreRead, err)
	}
	if audit.Fingerprint != current {
		return nil, fmt.Errorf("thiết lập nền tảng đã thay đổi; hãy gọi lại novel_context để lấy fingerprint mới nhất rồi mới xem xét: %w", errs.ErrToolConflict)
	}
	if audit.Ready && len(audit.Issues) > 0 {
		return nil, fmt.Errorf("khi ready=true thì issues phải rỗng: %w", errs.ErrToolArgs)
	}
	if !audit.Ready && len(audit.Issues) == 0 {
		return nil, fmt.Errorf("khi ready=false phải đưa ra issues cụ thể: %w", errs.ErrToolArgs)
	}
	for i, issue := range audit.Issues {
		if strings.TrimSpace(issue.Artifact) == "" || strings.TrimSpace(issue.Description) == "" || strings.TrimSpace(issue.Evidence) == "" {
			return nil, fmt.Errorf("issues[%d] phải chứa artifact, description và evidence: %w", i, errs.ErrToolArgs)
		}
	}

	if err := t.store.Outline.SaveFoundationAudit(audit); err != nil {
		return nil, fmt.Errorf("save foundation audit: %w: %w", errs.ErrStoreWrite, err)
	}
	result := map[string]any{
		"foundation_ready": audit.Ready,
		"issues":           audit.Issues,
	}
	if !audit.Ready {
		result["next_action"] = "sửa các thiết lập nền tảng tương ứng theo issues, gọi lại novel_context rồi xem xét lại"
		return json.Marshal(result)
	}

	if _, err := t.store.Checkpoints.AppendArtifact(domain.GlobalScope(), "foundation_audit", "meta/foundation_audit.json"); err != nil {
		return nil, fmt.Errorf("checkpoint foundation audit: %w: %w", errs.ErrStoreWrite, err)
	}
	if err := t.store.Progress.UpdatePhase(domain.PhaseWriting); err != nil {
		return nil, fmt.Errorf("enter writing phase: %w: %w", errs.ErrStoreWrite, err)
	}
	result["phase"] = string(domain.PhaseWriting)
	return json.Marshal(result)
}
