package domain

import (
	"fmt"
	"unicode/utf8"
)

// ReviewInterval khoảng xem xét toàn cục (kích hoạt mỗi N chương).
const ReviewInterval = 5

// ShouldReview theo số chương đã hoàn thành xác định có cần xem xét toàn cục không (chế độ truyện ngắn/vừa).
func ShouldReview(completedCount int) (bool, string) {
	if completedCount > 0 && completedCount%ReviewInterval == 0 {
		return true, fmt.Sprintf("đã hoàn thành %d chương, kích hoạt xem xét toàn cục", completedCount)
	}
	return false, ""
}

// ShouldArcReview ở chế độ truyện dài xác định có cần xem xét cấp cung/cấp tập không.
func ShouldArcReview(isArcEnd, isVolumeEnd bool, volume, arc int) (bool, string) {
	if isVolumeEnd {
		return true, fmt.Sprintf("kết thúc tập %d, cung %d (kết thúc tập), kích hoạt xem xét cấp cung + cấp tập", volume, arc)
	}
	if isArcEnd {
		return true, fmt.Sprintf("kết thúc tập %d, cung %d, kích hoạt xem xét cấp cung", volume, arc)
	}
	return false, ""
}

// WordCount đếm số ký tự theo rune.
func WordCount(content string) int {
	return utf8.RuneCountInString(content)
}
