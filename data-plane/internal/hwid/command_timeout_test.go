package hwid

import (
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// P2 L6 (timeout part): a helper command that hangs is killed after commandTimeout.
func TestHelperCommandsAreBounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sleep(1)")
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	old := commandTimeout
	commandTimeout = 100 * time.Millisecond
	defer func() { commandTimeout = old }()
	start := time.Now()
	if _, err := commandOutput(sleep, "10"); err == nil {
		t.Fatal("a hung command must fail")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("the command was not stopped at its timeout")
	}
}
