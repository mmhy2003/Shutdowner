//go:build !windows

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// readSecret echoes off Windows. --hash-password is a Windows operation; this
// exists only so the command is usable during development.
func readSecret(prompt string) (string, error) {
	fmt.Print(prompt + "(input is visible on this platform) ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
