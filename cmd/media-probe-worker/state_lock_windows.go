package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func lockNodeStateFile(file *os.File) error {
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errNodeStateLocked
	}
	if err != nil {
		return fmt.Errorf("获取节点状态锁失败: %w", err)
	}
	return nil
}
