package logger

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSetupFileWritesDefaultLog(t *testing.T) {
	previous := slog.Default()

	dir := t.TempDir()
	cleanup, err := SetupFile(dir, "test.log", false,
		slog.String("version", "v1.2.3"),
		slog.String("commit", "abc123"),
		slog.String("built", "2026-08-03"),
	)
	if err != nil {
		t.Fatalf("SetupFile: %v", err)
	}
	slog.Info("logger-test-message")
	cleanup()
	if slog.Default() != previous {
		t.Fatal("cleanup phải khôi phục logger mặc định trước đó")
	}

	data, err := os.ReadFile(filepath.Join(dir, "logs", "test.log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "logger-test-message") {
		t.Fatalf("log missing message: %q", data)
	}
	if !regexp.MustCompile(`time=\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}(Z|[+-]\d{2}:\d{2})`).Match(data) {
		t.Fatalf("thời gian nhật ký phải chứa ngày, mili-giây và múi giờ: %q", data)
	}
	if !strings.Contains(string(data), "msg=\"bắt đầu phiên nhật ký\"") || !strings.Contains(string(data), "session=") {
		t.Fatalf("nhật ký phải chứa biên phiên có thể liên kết và thuộc tính session: %q", data)
	}
	for _, want := range []string{"version=v1.2.3", "commit=abc123", "built=2026-08-03"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("nhật ký phải chứa định danh bản dựng %q: %q", want, data)
		}
	}
}

func TestSetupFileReturnsOpenError(t *testing.T) {
	previous := slog.Default()
	var fallback bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&fallback, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cleanup, err := SetupFile(blocker, "test.log", false)
	if err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Fatal("phải trả về lỗi khi không tạo được thư mục nhật ký")
	}
	if cleanup != nil {
		t.Fatal("khi thất bại không được trả về hàm dọn dẹp")
	}
	slog.Info("fallback-remains-visible")
	if !strings.Contains(fallback.String(), "fallback-remains-visible") {
		t.Fatal("sau khi khởi tạo nhật ký tệp thất bại phải giữ logger mặc định ban đầu")
	}
}
