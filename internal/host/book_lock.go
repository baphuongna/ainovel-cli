package host

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

const bookLockFile = ".ainovel.lock"

// ErrBookInUse biểu thị cùng một thư mục tiểu thuyết đã bị tiến trình khác chiếm dụng.
var ErrBookInUse = errors.New("Thư mục tiểu thuyết đã bị một thực thể ainovel-cli khác chiếm dụng")

// bookLease giữ quyền độc chiếm xuyên tiến trình đối với thư mục tiểu thuyết trong toàn bộ vòng đời Host.
// File khóa giữ lại trong thư mục; trạng thái chiếm dụng thật do hệ điều hành quản lý, tiến trình thoát bất thường cũng tự nhả.
type bookLease struct {
	lock *flock.Flock
}

func acquireBookLease(dir string) (*bookLease, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("parse thư mục tiểu thuyết: %w", err)
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return nil, fmt.Errorf("tạo thư mục tiểu thuyết: %w", err)
	}
	fileLock := flock.New(filepath.Join(absDir, bookLockFile), flock.SetPermissions(0o600))
	locked, err := fileLock.TryLock()
	if err != nil {
		return nil, closeBookLockAfterFailure(fileLock, fmt.Errorf("chiếm dụng thư mục tiểu thuyết %q: %w", absDir, err))
	}
	if !locked {
		return nil, closeBookLockAfterFailure(fileLock, fmt.Errorf(
			"%w: %s; vui lòng đóng terminal khác đang thao tác thư mục này, hoặc dùng thư mục tiểu thuyết khác",
			ErrBookInUse,
			absDir,
		))
	}
	return &bookLease{lock: fileLock}, nil
}

func closeBookLockAfterFailure(fileLock *flock.Flock, cause error) error {
	if err := fileLock.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("đóng khóa thư mục tiểu thuyết: %w", err))
	}
	return cause
}

func (l *bookLease) Close() error {
	if l == nil || l.lock == nil {
		return nil
	}
	err := l.lock.Close()
	l.lock = nil
	return err
}
