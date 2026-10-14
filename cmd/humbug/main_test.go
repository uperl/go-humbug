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

func start(t *testing.T) *proc { t.Helper(); return startWith(t) }

func startWith(t *testing.T, args ...string) *proc {
	t.Helper()
	return startCmd(t, exec.Command(binary, args...))
}

// startCmd starts cmd, which runs a humbug binary, with its stdio connected for the test.
func startCmd(t *testing.T, cmd *exec.Cmd) *proc {
	t.Helper()
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

func validateBody(id int, body string) string {
	return fmt.Sprintf(`{"id":%d,"type":"validate","params":{"request":{"method":"POST","url":"http://example.test/users","headers":[["Content-Type","application/json"],["X-Request-Id","abc"]],"body_base64":"%s"}}}`, id, base64Std(body))
}

func resultOf(r map[string]any) map[string]any { return r["result"].(map[string]any) }

func sideOf(r map[string]any, side string) map[string]any { return resultOf(r)[side].(map[string]any) }

func reasons(side map[string]any) []string {
	var out []string
	for _, d := range side["diagnostics"].([]any) {
		if r, ok := d.(map[string]any)["reason"].(string); ok {
			out = append(out, r)
		}
	}
	return out
}

// A body of nested brackets must neither exhaust memory nor kill the process. The source-map
// parser once cost memory proportional to the square of the nesting depth: 100 KB took 3 GB and
// 1 MB got the process killed by the OS without a reply. Such a body is now refused before any
// parsing: its side is incomplete (body.too_deep).
func TestDeeplyNestedBodyGetsAReplyAndTheProcessSurvives(t *testing.T) {
	p := start(t)
	p.call(initCmd)

	begin := time.Now()
	r := p.call(validateBody(2, strings.Repeat("[", 5_000_000))) // a 5 MB body
	if r["type"] != "result" {
		t.Fatalf("reply: %v", r)
	}
	if d := time.Since(begin); d > 10*time.Second {
		t.Errorf("a nested body took %v to answer", d)
	}
	side := sideOf(r, "request")
	if side["status"] != "incomplete" || len(reasons(side)) != 1 || reasons(side)[0] != "body.too_deep" {
		t.Errorf("request: %v", side)
	}
	if resultOf(r)["valid"] == true {
		t.Errorf("an uninspected body must never be valid")
	}

	// Still alive and correct afterwards.
	if r := p.call(validateCmd(3, "valid")); r["type"] != "result" || resultOf(r)["valid"] != true {
		t.Errorf("after the nested body: %v", r)
	}
}

// The depth limit is configurable up to what the toolchain supports; within it, a deep document
// keeps its spans (the index must still be built and used).
func TestDeepButValidBodyKeepsItsSpans(t *testing.T) {
	const depth = 9000
	body := strings.Repeat("[", depth) + strings.Repeat("]", depth)
	p := startWith(t, "--max-nesting-depth", "10000")
	p.call(initCmd)
	side := sideOf(p.call(validateBody(2, body)), "request")
	var span map[string]any
	for _, d := range side["diagnostics"].([]any) {
		if dd := d.(map[string]any); dd["code"] == "schema.type" {
			span, _ = dd["instance"].(map[string]any)["span"].(map[string]any)
		}
	}
	// The root is an array where /users expects an object: the finding spans the whole document.
	if span == nil || span["part"] != "value" || span["start"] != float64(0) || span["end"] != float64(2*depth) {
		t.Errorf("root span: %v (diagnostics %v)", span, side["diagnostics"])
	}
}

// ---- command-line flags set the limits ----

func TestFlagsSetTheLimitsAndInitializeReportsThem(t *testing.T) {
	p := startWith(t, "--max-body-bytes", "50", "--max-headers=7", "--max-nesting-depth", "12")
	r := p.call(initCmd)
	lim, _ := resultOf(r)["limits"].(map[string]any)
	if lim["max_body_bytes"] != float64(50) || lim["max_headers"] != float64(7) || lim["max_nesting_depth"] != float64(12) {
		t.Fatalf("initialize must report the effective limits: %v", lim)
	}
	if lim["max_line_bytes"] != float64(64<<20) || lim["max_header_bytes"] != float64(1<<20) || lim["max_header_value_bytes"] != float64(64<<10) {
		t.Errorf("unset limits keep their defaults: %v", lim)
	}
	// And they are enforced: a 51-byte body is not inspected.
	side := sideOf(p.call(validateBody(2, strings.Repeat("x", 51))), "request")
	if side["status"] != "incomplete" || len(reasons(side)) != 1 || reasons(side)[0] != "body.too_large" {
		t.Errorf("request: %v", side)
	}
	// A body of exactly the limit is inspected (it is malformed JSON, so a finding).
	side = sideOf(p.call(validateBody(3, strings.Repeat("x", 50))), "request")
	if side["status"] != "invalid" {
		t.Errorf("a body at the limit is inspected: %v", side)
	}
}

func TestDefaultLimitsAreReported(t *testing.T) {
	p := start(t)
	lim := resultOf(p.call(initCmd))["limits"].(map[string]any)
	want := map[string]float64{"max_line_bytes": 64 << 20, "max_body_bytes": 16 << 20, "max_nesting_depth": 1000,
		"max_headers": 1000, "max_header_value_bytes": 64 << 10, "max_header_bytes": 1 << 20}
	for k, v := range want {
		if lim[k] != v {
			t.Errorf("%s = %v, want %v", k, lim[k], v)
		}
	}
}

// A usage error exits with status 2, explains itself on stderr, and writes nothing to stdout.
func TestBadFlagsAreUsageErrors(t *testing.T) {
	for name, args := range map[string][]string{
		"unknown flag":        {"--frobnicate"},
		"not a number":        {"--max-body-bytes", "lots"},
		"zero":                {"--max-headers", "0"},
		"negative":            {"--max-body-bytes", "-5"},
		"depth over ceiling":  {"--max-nesting-depth", "10001"},
		"positional argument": {"spec.yaml"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cmd := exec.Command(binary, args...)
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 2 {
				t.Errorf("exit: %v", err)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout must stay empty: %q", stdout.String())
			}
			if stderr.Len() == 0 {
				t.Errorf("a usage error must be explained on stderr")
			}
		})
	}
}

func TestHelpExitsCleanlyOnStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(binary, "--help")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Errorf("--help: %v", err)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "max-body-bytes") {
		t.Errorf("usage belongs on stderr and must list the flags: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
