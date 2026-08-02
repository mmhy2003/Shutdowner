//go:build windows

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// readSecret disables console echo through the Win32 console API rather than
// pulling in golang.org/x/term, which would be a fourth dependency.
func readSecret(prompt string) (string, error) {
	fmt.Print(prompt)

	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err == nil {
		_ = windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT)
		defer func() { _ = windows.SetConsoleMode(h, mode) }()
	}

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	fmt.Println()
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
