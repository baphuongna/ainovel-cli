// Package notify cung cấp kênh cảnh báo chế độ không người trực.
//
// Định vị hợp hiến (architecture.md §2.3): hành động thuần tầng quan sát — cảnh báo
// không bao giờ can thiệp luồng điều khiển (không thử lại, không đổi phân công,
// không dừng máy), chỉ "hô" các sự kiện vốn đã có trong TUI ra ngoài màn hình.
// Send chạy bất đồng bộ, không bao giờ chặn Host, thất bại chỉ ghi slog.
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Notification toàn bộ sự thật của một cảnh báo.
type Notification struct {
	Kind  string `json:"kind"`  // tên sự kiện ổn định do Kinds trả về
	Level string `json:"level"` // info / warn / error
	Title string `json:"title"`
	Body  string `json:"body"`
}

const (
	KindRunEnd        = "run_end"
	KindBudget        = "budget"
	KindAdvanceGate   = "advance_gate"
	KindStopGuard     = "stop_guard"
	KindPlanStart     = "plan_start"
	KindDeadlock      = "deadlock"
	KindWorkerFailure = "worker_failure"
)

// Kinds trả về toàn bộ tên sự kiện có thể dùng cho notify.events ở phiên bản hiện tại.
// Đây là nguồn sự thật duy nhất của hợp đồng sự kiện thông báo.
func Kinds() []string {
	return []string{
		KindRunEnd,
		KindBudget,
		KindAdvanceGate,
		KindStopGuard,
		KindPlanStart,
		KindDeadlock,
		KindWorkerFailure,
	}
}

func IsKnownKind(kind string) bool {
	for _, known := range Kinds() {
		if kind == known {
			return true
		}
	}
	return false
}

// Notifier phân phối thông báo theo cấu hình. Giá trị zero không dùng được, phải tạo
// qua New; an toàn với nil (Send là noop).
type Notifier struct {
	command string          // khác rỗng thì thay thế kênh system (đẩy về điện thoại đi qua đây)
	events  map[string]bool // nil = cho qua toàn bộ kind
	timeout time.Duration
}

// New tạo Notifier. command rỗng thì đi kênh system tích hợp (bong bóng thông báo
// Windows / osascript macOS / notify-send Linux); events khác rỗng thì chỉ cho qua
// các kind được liệt kê.
func New(command string, events []string) *Notifier {
	n := &Notifier{command: strings.TrimSpace(command), timeout: 10 * time.Second}
	if len(events) > 0 {
		n.events = make(map[string]bool, len(events))
		for _, ev := range events {
			n.events[ev] = true
		}
	}
	return n
}

// Send gửi bất đồng bộ một thông báo. Lọc, thực thi, xử lý thất bại đều không ảnh
// hưởng bên gọi.
func (n *Notifier) Send(nt Notification) {
	if !n.allows(nt.Kind) {
		return
	}
	go n.deliver(nt)
}

// allows trả về kind có được cho qua hay không (Notifier nil / không nằm trong
// events thì chặn).
func (n *Notifier) allows(kind string) bool {
	if n == nil {
		return false
	}
	return n.events == nil || n.events[kind]
}

// deliver thực thi đồng bộ một lần gửi và ghi lại thất bại, được Send gọi trong
// goroutine.
func (n *Notifier) deliver(nt Notification) {
	if err := n.deliverError(nt); err != nil {
		slog.Warn("gửi thông báo thất bại", "module", "notify", "kind", nt.Kind, "err", err)
	}
}

// deliverError thực thi đồng bộ một lần gửi và trả về lỗi gốc. Send gọi deliver
// trong goroutine để ghi thất bại; test gọi trực tiếp phương thức này, tránh lỗi
// bị che lấp bởi triệu chứng tầng hai.
func (n *Notifier) deliverError(nt Notification) error {
	ctx, cancel := context.WithTimeout(context.Background(), n.timeout)
	defer cancel()

	if n.command != "" {
		return runCommand(ctx, n.command, nt)
	}
	return runSystem(ctx, nt)
}

// runCommand thực thi lệnh người dùng cấu hình: các trường truyền qua biến môi trường
// (một dòng curl không phụ thuộc, không rủi ro tiêm), đồng thời ghi JSON đầy đủ vào
// stdin (kịch bản phân phối phức tạp tự phân tích). Quá hạn thì ctx ép giết.
func runCommand(ctx context.Context, command string, nt Notification) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		powershell, err := findPowerShell()
		if err != nil {
			return err
		}
		cmd = exec.CommandContext(ctx, powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Env = notificationEnv(nt)
	payload, _ := json.Marshal(nt)
	cmd.Stdin = strings.NewReader(string(payload))
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("lệnh thông báo quá hạn: %w", ctxErr)
		}
		return err
	}
	return nil
}

func notificationEnv(nt Notification) []string {
	return append(os.Environ(),
		"NOTIFY_KIND="+nt.Kind,
		"NOTIFY_LEVEL="+nt.Level,
		"NOTIFY_TITLE="+nt.Title,
		"NOTIFY_BODY="+nt.Body,
	)
}

// runSystem thông báo desktop tích hợp: chỉ phủ kịch bản "người ngồi cạnh máy",
// không tìm thấy lệnh thì hạ cấp im lặng.
func runSystem(ctx context.Context, nt Notification) error {
	switch runtime.GOOS {
	case "windows":
		return runWindowsNotification(ctx, nt)
	case "darwin":
		script := "display notification " + appleScriptString(nt.Body) + " with title " + appleScriptString(nt.Title)
		return exec.CommandContext(ctx, "osascript", "-e", script).Run()
	case "linux":
		if _, err := exec.LookPath("notify-send"); err != nil {
			slog.Info("thông báo hạ cấp thành nhật ký (không có notify-send)", "module", "notify", "title", nt.Title, "body", nt.Body)
			return nil
		}
		return exec.CommandContext(ctx, "notify-send", nt.Title, nt.Body).Run()
	default:
		slog.Info("thông báo hạ cấp thành nhật ký (nền tảng không có kênh system)", "module", "notify", "title", nt.Title, "body", nt.Body)
		return nil
	}
}

// runWindowsNotification dùng PowerShell + WinForms NotifyIcon sẵn có của hệ thống.
// Windows 10/11 hiển thị bong bóng ở góc trên phải và đưa vào trải nghiệm thông báo hệ
// thống; không cần cài module, đăng ký ứng dụng hay mang thêm tệp nhị phân. Bên gọi vốn
// chạy bất đồng bộ, giữ tiến trình ngắn chỉ để hệ thống kịp nhận thông điệp bong bóng.
func runWindowsNotification(ctx context.Context, nt Notification) error {
	powershell, err := findPowerShell()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, powershell,
		"-NoLogo", "-NoProfile", "-NonInteractive", "-STA", "-Command", windowsNotificationScript)
	cmd.Env = notificationEnv(nt)
	return cmd.Run()
}

func findPowerShell() (string, error) {
	// Ưu tiên PowerShell 7: trên GitHub Windows runner và môi trường Windows hiện đại,
	// pwsh ứng xử với stdin bị chuyển hướng ổn định hơn; Windows PowerShell 5.1 chỉ làm
	// phương án dự phòng tương thích.
	for _, name := range []string{"pwsh.exe", "pwsh", "powershell.exe", "powershell"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("Thông báo Windows cần PowerShell, nhưng hệ thống không tìm thấy powershell.exe hoặc pwsh.exe")
}

const windowsNotificationScript = `$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$notify = New-Object System.Windows.Forms.NotifyIcon
$notify.Icon = [System.Drawing.SystemIcons]::Information
$notify.BalloonTipTitle = $env:NOTIFY_TITLE
$notify.BalloonTipText = $env:NOTIFY_BODY
$notify.BalloonTipIcon = switch ($env:NOTIFY_LEVEL) {
  'error' { [System.Windows.Forms.ToolTipIcon]::Error; break }
  'warn'  { [System.Windows.Forms.ToolTipIcon]::Warning; break }
  default { [System.Windows.Forms.ToolTipIcon]::Info }
}
$notify.Visible = $true
$notify.ShowBalloonTip(4000)
Start-Sleep -Milliseconds 4500
$notify.Dispose()`

// appleScriptString bọc văn bản bất kỳ thành chuỗi literal AppleScript.
func appleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
