//go:build unix

package main

import (
	"os"
	"syscall"
	"unsafe"
)

func terminalWidth() int {
	info, err := os.Stdout.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return 80
	}
	var size struct {
		Row    uint16
		Col    uint16
		Xpixel uint16
		Ypixel uint16
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&size)))
	if errno != 0 || size.Col < 40 {
		return 80
	}
	return int(size.Col)
}
