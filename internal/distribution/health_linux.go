package distribution

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Kernel process start ticks and executable inode bind a readiness record to
// the exact managed child, including PID reuse and early core exit.
func processIdentity(pid int, expected string) (string, error) {
	if pid <= 0 {
		return "", errors.New("invalid process PID")
	}
	prefix := "/proc/" + strconv.Itoa(pid)
	data, err := os.ReadFile(prefix + "/stat")
	if err != nil {
		return "", err
	}
	end := strings.LastIndex(string(data), ") ")
	if end < 0 || len(data) > 4096 {
		return "", errors.New("invalid process status")
	}
	fields := strings.Fields(string(data[end+2:]))
	if len(fields) < 20 || fields[0] == "Z" || fields[0] == "X" {
		return "", errors.New("process has exited")
	}
	info, err := os.Stat(expected)
	if err != nil {
		return "", err
	}
	actual, err := os.Stat(prefix + "/exe")
	if err != nil || !os.SameFile(info, actual) {
		return "", fmt.Errorf("process executable differs from installed pair")
	}
	return fields[19], nil
}
