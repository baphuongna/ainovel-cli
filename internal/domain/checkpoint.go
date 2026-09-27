package domain

import (
	"fmt"
	"time"
)

// ScopeKind định danh loại phạm vi của checkpoint.
type ScopeKind string

const (
	ScopeChapter ScopeKind = "chapter"
	ScopeArc     ScopeKind = "arc"
	ScopeVolume  ScopeKind = "volume"
	ScopeGlobal  ScopeKind = "global"
)

// Scope định vị phạm vi sáng tác mà một checkpoint thuộc về.
type Scope struct {
	Kind    ScopeKind `json:"kind"`
	Chapter int       `json:"chapter,omitempty"`
	Volume  int       `json:"volume,omitempty"`
	Arc     int       `json:"arc,omitempty"`
}

// ChapterScope dựng một Scope cấp chương.
func ChapterScope(chapter int) Scope {
	return Scope{Kind: ScopeChapter, Chapter: chapter}
}

// ArcScope dựng một Scope cấp cung.
func ArcScope(volume, arc int) Scope {
	return Scope{Kind: ScopeArc, Volume: volume, Arc: arc}
}

// VolumeScope dựng một Scope cấp tập.
func VolumeScope(volume int) Scope {
	return Scope{Kind: ScopeVolume, Volume: volume}
}

// GlobalScope dựng một Scope toàn cục.
func GlobalScope() Scope {
	return Scope{Kind: ScopeGlobal}
}

func (s Scope) String() string {
	switch s.Kind {
	case ScopeChapter:
		return fmt.Sprintf("chapter:%d", s.Chapter)
	case ScopeArc:
		return fmt.Sprintf("arc:v%da%d", s.Volume, s.Arc)
	case ScopeVolume:
		return fmt.Sprintf("volume:%d", s.Volume)
	default:
		return "global"
	}
}

// Matches xác định hai Scope có giống nhau không.
func (s Scope) Matches(other Scope) bool {
	if s.Kind != other.Kind {
		return false
	}
	switch s.Kind {
	case ScopeChapter:
		return s.Chapter == other.Chapter
	case ScopeArc:
		return s.Volume == other.Volume && s.Arc == other.Arc
	case ScopeVolume:
		return s.Volume == other.Volume
	default:
		return true
	}
}

// Checkpoint ghi lại sự kiện một step hoàn thành thành công.
// Do công cụ nối thêm vào JSONL sau khi lưu xuống đĩa nguyên tử, là nguồn sự kiện duy nhất
// cho khôi phục và quan sát.
type Checkpoint struct {
	Seq        int64     `json:"seq"`
	Scope      Scope     `json:"scope"`
	Step       string    `json:"step"`
	Artifact   string    `json:"artifact,omitempty"`
	Digest     string    `json:"digest,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}
