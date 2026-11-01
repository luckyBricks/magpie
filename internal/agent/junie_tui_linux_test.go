package agent

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/proc"
	"golang.org/x/sys/unix"
)

func init() { junieInteractive = runJunieTUI }

// A real terminal exercises the TUI model resolver and its fasterModel call.
// Read readiness and the completed answer from the screen, without sleeps.
func runJunieTUI(t *testing.T, cmd *exec.Cmd) (string, error) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer master.Close()
	fd := int(master.Fd())
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		return "", err
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		return "", err
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return "", err
	}
	defer slave.Close()
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 40, Col: 140}); err != nil {
		return "", err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	tree, err := proc.StartTree(cmd)
	if err != nil {
		return "", err
	}
	slave.Close()
	chunks := make(chan []byte)
	done := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(chunks)
		for {
			buf := make([]byte, 32<<10)
			n, err := master.Read(buf)
			if n > 0 {
				select {
				case chunks <- buf[:n]:
				case <-done:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		close(done)
		// Junie may have a shell helper; end its whole PTY process group.
		tree.Kill()
		tree.Wait()
		master.Close()
		<-readerDone
	}()
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	var screen bytes.Buffer
	sent := false
	trusted := false
	imported := false
	cursorQuery := -1
	lastScreen := func() string {
		b := screen.Bytes()
		if len(b) > 8192 {
			b = b[len(b)-8192:]
		}
		return string(b)
	}
	for {
		select {
		case b, ok := <-chunks:
			if !ok {
				return lastScreen(), fmt.Errorf("Junie closed its terminal")
			}
			screen.Write(b)
			if q := bytes.LastIndex(screen.Bytes(), []byte("\x1b[6n")); q > cursorQuery {
				master.Write([]byte("\x1b[1;1R"))
				cursorQuery = q
			}
			if !trusted && bytes.Contains(screen.Bytes(), []byte("Do you trust the contents of this project?")) {
				if _, err := master.Write([]byte("\r")); err != nil {
					return lastScreen(), err
				}
				trusted = true
			}
			if !imported && bytes.Contains(screen.Bytes(), []byte("Import Project Configuration")) {
				if _, err := master.Write([]byte("\r")); err != nil {
					return lastScreen(), err
				}
				imported = true
			}
			// The input can render behind the trust/import dialogs. Wait
			// for the import result before sending the task to that input.
			if !sent && bytes.Contains(screen.Bytes(), []byte("Imported configuration from")) &&
				(bytes.Contains(screen.Bytes(), []byte("Type your prompt")) || bytes.Contains(screen.Bytes(), []byte("What can I do?"))) {
				if _, err := master.Write([]byte("Create pong.txt containing pong\r")); err != nil {
					return lastScreen(), err
				}
				sent = true
			}
			if bytes.Contains(screen.Bytes(), []byte("Wrote pong.txt.")) {
				return "", nil
			}
		case <-deadline.C:
			return lastScreen(), fmt.Errorf("Junie never completed the TUI task")
		}
	}
}
