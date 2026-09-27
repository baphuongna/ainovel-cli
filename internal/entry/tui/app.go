package tui

import (
	"fmt"
	"log/slog"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host"
	buildversion "github.com/voocel/ainovel-cli/internal/version"
)

// Run khởi động TUI.
// Quy ước phân tầng chế độ khởi động:
//  1. Chế độ nhanh, chế độ đồng sáng tạo thuộc "điều phối khởi động";
//  2. Phiên sáng tác chính thức vào host.Host;
//  3. Sau này nếu thêm chế độ chia sẻ như "viết tiếp tiểu thuyết sẵn có", quy về
//     internal/entry/startup.
func Run(cfg bootstrap.Config, bundle assets.Bundle, build buildversion.Info) error {
	rt, err := host.New(cfg, bundle, host.WithFileLog("tui.log", false,
		slog.String("version", build.Version),
		slog.String("commit", build.Commit),
		slog.String("built", build.Date),
	))
	if err != nil {
		return err
	}
	defer rt.Close()

	m := NewModel(rt, build.Version)
	if logErr := rt.FileLogError(); logErr != nil {
		logWarning := fmt.Errorf("nhật ký file không khả dụng, tiếp tục dùng nhật ký terminal: %w", logErr)
		m.err = logWarning
		m.applyEvent(host.Event{
			Time: time.Now(), Category: "SYSTEM", Level: "warn",
			Summary: logWarning.Error(), Detail: logWarning.Error(),
		})
	}
	// Không bật toàn cục báo chuột lúc khởi động: trang chào không dùng chuột, tắt báo
	// giữ được kéo chọn copy gốc của terminal. Khi vào bàn sáng tác (modeRunning) thì
	// enterRunning mới bật báo, để hỗ trợ click đổi bảng / con lăn / kéo thanh bên.
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}
