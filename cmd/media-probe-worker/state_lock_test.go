package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestNodeStateLockKeepsFileAndRejectsOtherOwner(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".node.lock")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	first, err := lockNodeState(directory)
	if err != nil {
		t.Fatalf("unowned file from a previous process blocked startup: %v", err)
	}
	defer first.Close()
	if second, err := lockNodeState(directory); !errors.Is(err, errNodeStateLocked) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("second owner was not rejected: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file must keep its inode after release: %v", err)
	}
	reopened, err := lockNodeState(directory)
	if err != nil {
		t.Fatalf("closed owner blocked restart: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNodeStateLockReleasedAfterProcessKill(t *testing.T) {
	directory := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNodeStateLockHelperProcess$")
	child.Env = append(os.Environ(), "MEDIA_PROBE_LOCK_TEST_DIRECTORY="+directory)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		ready <- scanner.Scan() && scanner.Text() == "locked"
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("child failed to acquire lock")
		}
	case <-ctx.Done():
		t.Fatal("child did not report lock ownership")
	}
	if file, err := lockNodeState(directory); !errors.Is(err, errNodeStateLocked) {
		if file != nil {
			file.Close()
		}
		t.Fatalf("running process did not retain exclusive lock: %v", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = child.Wait()
	waited = true
	if err == nil {
		t.Fatal("child did not terminate abnormally")
	}
	if _, err := os.Stat(filepath.Join(directory, ".node.lock")); err != nil {
		t.Fatalf("test must restart with the original lock file present: %v", err)
	}
	file, err := lockNodeState(directory)
	if err != nil {
		t.Fatalf("killed process left state locked: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNodeStateLockHelperProcess(t *testing.T) {
	directory := os.Getenv("MEDIA_PROBE_LOCK_TEST_DIRECTORY")
	if directory == "" {
		return
	}
	file, err := lockNodeState(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	fmt.Println("locked")
	for {
		time.Sleep(time.Hour)
	}
}
