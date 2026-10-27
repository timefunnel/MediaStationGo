//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func lockNodeStateFile(file *os.File) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return errNodeStateLocked
	}
	if err != nil {
		return fmt.Errorf("获取节点状态锁失败: %w", err)
	}
	return nil
}
