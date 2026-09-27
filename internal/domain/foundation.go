package domain

// FoundationAuditIssue là vấn đề nhất quán xuyên tệp mà Architect đưa ra cho thiết lập nền tảng
// đã lưu xuống đĩa.
type FoundationAuditIssue struct {
	Artifact    string `json:"artifact"`
	Description string `json:"description"`
	Evidence    string `json:"evidence"`
	Suggestion  string `json:"suggestion,omitempty"`
}

// FoundationAudit ghi lại một lượt kiểm tra bằng model nhắm vào thiết lập nền tảng phiên bản xác định.
type FoundationAudit struct {
	Fingerprint string                 `json:"fingerprint"`
	Ready       bool                   `json:"ready"`
	Summary     string                 `json:"summary"`
	Issues      []FoundationAuditIssue `json:"issues"`
}
