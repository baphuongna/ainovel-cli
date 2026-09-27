package host

import "log/slog"

type newOptions struct {
	logFile       string
	logAlsoStderr bool
	logAttrs      []slog.Attr
}

// NewOption cấu hình quá trình dựng Host, tài nguyên runtime vẫn do Host giữ.
type NewOption func(*newOptions)

// WithFileLog cho Host giữ một phiên log runtime. Log chỉ mở sau khi lấy lease thư mục tiểu thuyết,
// và đóng sau khi mọi log đóng của Host hoàn tất. Mở thất bại thì tiếp tục dùng logger tiến trình hiện tại,
// bên gọi phải xử lý lỗi này tường minh qua FileLogError.
func WithFileLog(filename string, alsoStderr bool, attrs ...slog.Attr) NewOption {
	return func(opts *newOptions) {
		opts.logFile = filename
		opts.logAlsoStderr = alsoStderr
		opts.logAttrs = append([]slog.Attr(nil), attrs...)
	}
}
