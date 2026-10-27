//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package main

import (
	"errors"
	"os"
)

func lockNodeStateFile(*os.File) error {
	return errors.New("当前操作系统不支持节点状态锁")
}
