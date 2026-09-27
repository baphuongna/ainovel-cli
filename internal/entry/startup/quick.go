package startup

import (
	"fmt"
	"os"
	"strings"
)

// LoadPromptFile đọc tệp làm yêu cầu sáng tác ban đầu.
func LoadPromptFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("đọc prompt thất bại: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// PrepareQuick chỉnh lý prompt khởi động nhanh.
func PrepareQuick(rawPrompt string) (string, error) {
	prompt := strings.TrimSpace(rawPrompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	return prompt, nil
}
