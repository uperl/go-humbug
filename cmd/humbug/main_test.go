package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests run the real executable as a subprocess, the way a client would.

const (
	specPath = "../../testdata/specs/users.yaml"
	timeout  = 10 * time.Second
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "humbug-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "humbug")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type proc struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan string // stdout lines; closed at EOF
	stderr *bytes.Buffer
	// raw collects every byte written to stdout, for contamination checks.
	raw *bytes.Buffer
}

func start(t *testing.T) *proc {
	t.Helper()
	cmd := exec.Command(binary)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p := &proc{t: t, cmd: cmd, stdin: stdin, lines: make(chan string, 1024), stderr: &bytes.Buffer{}, raw: &bytes.Buffer{}}
	cmd.Stderr = p.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	go func() {
		defer close(p.lines)
		br := bufio.NewReaderSize(io.TeeReader(stdout, p.raw), 1<<20)
		for {
			l, err := br.ReadString('\n')
			if len(l) > 0 {
				p.lines <- strings.TrimSuffix(l, "\n")
			}
			if err != nil {
				return
			}
		}
	}()
	return p
}

func (p *proc) send(line string) {
	p.t.Helper()
	if _, err := io.WriteString(p.stdin, line+"\n"); err != nil {
		p.t.Fatalf("write: %v", err)
	}
}

// recv returns the next stdout line decoded as JSON.
func (p *proc) recv() map[string]any {
	p.t.Helper()
	select {
	case l, ok := <-p.lines:
		if !ok {
			p.t.Fatalf("stdout closed before a reply; stderr: %s", p.stderr)
		}
		var o map[string]any
		if err := json.Unmarshal([]byte(l), &o); err != nil {
			p.t.Fatalf("stdout line is not JSON: %q", l)
		}
		return o
	case <-time.After(timeout):
		p.t.Fatalf("timed out waiting for a reply; stderr: %s", p.stderr)
	}
	return nil
}

func (p *proc) call(line string) map[string]any { p.t.Helper(); p.send(line); return p.recv() }

// wait waits for exit and returns the error from cmd.Wait (nil = exit status 0).
func (p *proc) wait() error {
	p.t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		p.cmd.Process.Kill()
		p.t.Fatalf("process did not exit; stderr: %s", p.stderr)
	}
	return nil
}

// assertStdoutIsProtocolOnly fails if any stdout line is not a JSON object.
func (p *proc) assertStdoutIsProtocolOnly() {
	p.t.Helper()
	for _, l := range strings.Split(strings.TrimSuffix(p.raw.String(), "\n"), "\n") {
		if l == "" {
			continue
		}
		var o map[string]any
		if json.Unmarshal([]byte(l), &o) != nil || o["id"] == nil && o["type"] == nil {
			p.t.Errorf("non-protocol output on stdout: %q", l)
		}
		if strings.Contains(l, "\x1b[") {
			p.t.Errorf("ANSI escape on stdout: %q", l)
		}
	}
}

const initCmd = `{"id":1,"type":"initialize","params":{"protocol_version":1,"spec":{"path":"` + specPath + `"}}}`

func validateCmd(id any, status string) string {
	body := `{\"name\":\"Ann\",\"age\":30,\"profile\":{\"bio\":\"x\"}}`
	if status == "invalid" {
		body = `{\"name\":\"Ann\",\"age\":\"x\",\"profile\":{\"bio\":\"x\"}}`
	}
	b64 := func(s string) string { return strings.TrimSpace(base64Std(strings.ReplaceAll(s, `\"`, `"`))) }
	return fmt.Sprintf(`{"id":%v,"type":"validate","params":{"request":{"method":"POST","url":"http://example.test/users","headers":[["Content-Type","application/json"],["X-Request-Id","abc"]],"body_base64":"%s"}}}`, id, b64(body))
}

func TestMultipleCommandsThenShutdown(t *testing.T) {
	p := start(t)
	if r := p.call(initCmd); r["type"] != "result" || r["id"] != float64(1) {
		t.Fatalf("init: %v", r)
	}
	for i, want := range []bool{true, false, true} {
		kind := "valid"
		if !want {
			kind = "invalid"
		}
		r := p.call(validateCmd(fmt.Sprintf("%q", fmt.Sprint("req-", i)), kind))
		if r["id"] != fmt.Sprint("req-", i) || r["type"] != "result" || r["result"].(map[string]any)["valid"] != want {
			t.Fatalf("validate %d: %v", i, r)
		}
	}
	if r := p.call(`{"id":99,"type":"shutdown"}`); r["type"] != "result" || r["id"] != float64(99) {
		t.Fatalf("shutdown: %v", r)
	}
	// Anything after shutdown is ignored.
	p.send(validateCmd(100, "valid"))
	if err := p.wait(); err != nil {
		t.Errorf("exit after shutdown: %v", err)
	}
	if extra, ok := <-p.lines; ok {
		t.Errorf("reply after shutdown: %q", extra)
	}
	if p.stderr.Len() != 0 {
		t.Errorf("unexpected stderr on a clean run: %q", p.stderr)
	}
	p.assertStdoutIsProtocolOnly()
}

func TestEOFExitsCleanlyWithoutShutdown(t *testing.T) {
	p := start(t)
	p.call(initCmd)
	p.stdin.Close()
	if err := p.wait(); err != nil {
		t.Errorf("exit on EOF: %v", err)
	}
	if p.stderr.Len() != 0 {
		t.Errorf("stderr: %q", p.stderr)
	}
}

func TestEOFBeforeAnyCommand(t *testing.T) {
	p := start(t)
	p.stdin.Close()
	if err := p.wait(); err != nil {
		t.Errorf("exit on immediate EOF: %v", err)
	}
	if p.raw.Len() != 0 {
		t.Errorf("stdout not empty: %q", p.raw)
	}
}

func TestFinalLineWithoutNewlineIsProcessed(t *testing.T) {
	p := start(t)
	io.WriteString(p.stdin, `{"id":5,"type":"shutdown"}`) // no trailing newline, then EOF
	p.stdin.Close()
	r := p.recv()
	if r["id"] != float64(5) || r["type"] != "result" {
		t.Errorf("reply: %v", r)
	}
	if err := p.wait(); err != nil {
		t.Error(err)
	}
}

func TestManySequentialCommandsKeepOrderAndIDs(t *testing.T) {
	p := start(t)
	p.call(initCmd)
	const n = 200
	// Write and read concurrently, as the protocol requires of clients.
	go func() {
		for i := 0; i < n; i++ {
			kind := "valid"
			if i%3 == 0 {
				kind = "invalid"
			}
			io.WriteString(p.stdin, validateCmd(1000+i, kind)+"\n")
		}
	}()
	for i := 0; i < n; i++ {
		r := p.recv()
		if r["id"] != float64(1000+i) {
			t.Fatalf("reply %d has id %v", i, r["id"])
		}
		if valid := r["result"].(map[string]any)["valid"]; valid != (i%3 != 0) {
			t.Fatalf("reply %d: valid=%v", i, valid)
		}
	}
	p.stdin.Close()
	if err := p.wait(); err != nil {
		t.Error(err)
	}
	p.assertStdoutIsProtocolOnly()
}

func TestMalformedLinesDoNotCorruptTheStream(t *testing.T) {
	p := start(t)
	p.call(initCmd)
	for _, bad := range []string{`{"id":2,`, `not json`, ``, `{"id":3,"type":"nope"}`} {
		r := p.call(bad)
		if r["type"] != "error" {
			t.Errorf("%q: %v", bad, r)
		}
	}
	// Still usable afterwards.
	if r := p.call(validateCmd(7, "valid")); r["id"] != float64(7) || r["type"] != "result" {
		t.Errorf("after errors: %v", r)
	}
	p.stdin.Close()
	p.wait()
	p.assertStdoutIsProtocolOnly()
}

// Library logging (pb33f logs reference errors via slog) must go to stderr only.
func TestLibraryLoggingNeverReachesStdout(t *testing.T) {
	spec := filepath.Join(t.TempDir(), "bad.yaml")
	os.WriteFile(spec, []byte("openapi: 3.1.0\ninfo: {title: t, version: '1'}\npaths:\n  /x:\n    get:\n      responses:\n        '200':\n          description: ok\n          content:\n            application/json:\n              schema: {$ref: '#/components/schemas/Nope'}\n"), 0o644)
	p := start(t)
	r := p.call(`{"id":1,"type":"initialize","params":{"protocol_version":1,"spec":{"path":"` + spec + `"}}}`)
	if r["type"] != "error" || r["error"].(map[string]any)["code"] != "spec.invalid" {
		t.Fatalf("init: %v", r)
	}
	p.stdin.Close()
	p.wait()
	p.assertStdoutIsProtocolOnly()
	if len(strings.Split(strings.TrimSpace(p.raw.String()), "\n")) != 1 {
		t.Errorf("expected exactly one stdout line, got %q", p.raw)
	}
}

// SIGTERM is not handled specially: the Go runtime's default applies and the process
// terminates immediately by signal. No partial protocol line may be left on stdout.
func TestSIGTERMTerminatesPromptly(t *testing.T) {
	p := start(t)
	p.call(initCmd)
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err := p.wait()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected the process to be killed by SIGTERM, got %v", err)
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Errorf("exit status: %v", ee)
	}
	p.assertStdoutIsProtocolOnly()
}

// If the client goes away without closing stdin (its stdout reader is closed), the
// sidecar must not hang: a failed write ends the process.
func TestClientClosingStdoutEndsProcess(t *testing.T) {
	cmd := exec.Command(binary)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stdout.Close()
	io.WriteString(stdin, initCmd+"\n")
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected a non-zero exit when stdout is gone")
		}
	case <-time.After(timeout):
		cmd.Process.Kill()
		t.Fatal("process hung after its stdout was closed")
	}
}

func TestIdleUntilFirstCommand(t *testing.T) {
	// The process must neither exit nor print until it receives a command.
	p := start(t)
	select {
	case l, ok := <-p.lines:
		t.Fatalf("unsolicited output (%v): %q", ok, l)
	case <-time.After(300 * time.Millisecond):
	}
	p.stdin.Close()
	if err := p.wait(); err != nil {
		t.Error(err)
	}
}
