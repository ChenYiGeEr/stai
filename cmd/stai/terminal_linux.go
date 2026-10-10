//go:build linux

package main

import (
	"os"
	"sync"
	"syscall"
	"unsafe"
)

// isTerminal reports whether fd is an interactive terminal.
func isTerminal(fd uintptr) bool {
	var termios syscall.Termios
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&termios)))
	return err == 0
}

var (
	echoMu       sync.Mutex
	savedTermios syscall.Termios
	savedEcho    bool
)

// disableEcho turns off terminal echo for the current stdin.
func disableEcho() error {
	echoMu.Lock()
	defer echoMu.Unlock()
	if savedEcho {
		return nil
	}
	fd := os.Stdin.Fd()
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&savedTermios))); errno != 0 {
		return errno
	}
	noecho := savedTermios
	noecho.Lflag &^= syscall.ECHO
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&noecho))); errno != 0 {
		return errno
	}
	savedEcho = true
	return nil
}

// enableEcho restores the terminal echo state saved by disableEcho.
// It is safe for concurrent use; concurrent callers serialize on echoMu and
// only the first one performs the restore.
func enableEcho() error {
	echoMu.Lock()
	defer echoMu.Unlock()
	if !savedEcho {
		return nil
	}
	fd := os.Stdin.Fd()
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&savedTermios))); errno != 0 {
		return errno
	}
	savedEcho = false
	return nil
}
