package domain

import "slices"

var (
	hookTypes       = []string{"crisis", "mystery", "desire", "emotion", "choice"}
	dominantStrands = []string{"quest", "fire", "constellation"}
)

// HookTypes trả về phân loại móc chương. Trả về bản sao, bên gọi không được sửa từ điển miền.
func HookTypes() []string { return slices.Clone(hookTypes) }

// DominantStrands trả về phân loại tuyến tự sự chủ đạo của chương.
func DominantStrands() []string { return slices.Clone(dominantStrands) }

func ValidHookType(value string) bool       { return slices.Contains(hookTypes, value) }
func ValidDominantStrand(value string) bool { return slices.Contains(dominantStrands, value) }
