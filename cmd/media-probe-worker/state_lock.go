package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var errNodeStateLocked = errors.New("节点状态目录已有进程使用")

func lockNodeState(directory string) (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(directory, ".node.lock"), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("打开节点状态锁失败: %w", err)
	}
	if err := lockNodeStateFile(file); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	// Keep the same file inode across restarts. The OS owns the lock for this
	// open handle and releases it on close, process death or host restart.
	return file, nil
}
