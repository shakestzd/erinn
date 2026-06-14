package gate

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

const gateTmpDirName = ".test-tmp"

func GateExecEnv(codeRoot string) (env []string, redirected bool, tmpDir string) {
	env = os.Environ()
	tmp := EffectiveTmpDir(env)
	if tmp != "" && DirIsExecCapable(tmp) {
		return env, false, tmp
	}
	dir := filepath.Join(codeRoot, gateTmpDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return env, false, tmp
	}
	if !DirIsExecCapable(dir) {
		return env, false, tmp
	}
	env = UpsertEnv(env, "GOTMPDIR", dir)
	if !goCacheUsable(env) {
		cache := filepath.Join(dir, "gocache")
		if err := os.MkdirAll(cache, 0o755); err == nil && DirIsExecCapable(cache) {
			env = UpsertEnv(env, "GOCACHE", cache)
		}
	}
	return env, true, dir
}

func EffectiveTmpDir(env []string) string {
	if v := LookupEnv(env, "GOTMPDIR"); strings.TrimSpace(v) != "" {
		return v
	}
	if v := LookupEnv(env, "TMPDIR"); strings.TrimSpace(v) != "" {
		return v
	}
	return "/tmp"
}

func goCacheUsable(env []string) bool {
	c := strings.TrimSpace(LookupEnv(env, "GOCACHE"))
	if c == "" || c == "off" {
		return true
	}
	return DirIsExecCapable(c)
}

func DirIsExecCapable(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	execCapableCacheMu.Lock()
	if v, ok := execCapableCache[abs]; ok {
		execCapableCacheMu.Unlock()
		return v
	}
	execCapableCacheMu.Unlock()
	result := probeExecCapable(abs)
	execCapableCacheMu.Lock()
	execCapableCache[abs] = result
	execCapableCacheMu.Unlock()
	return result
}

var (
	execCapableCache   = map[string]bool{}
	execCapableCacheMu sync.Mutex
)

func probeExecCapable(dir string) bool {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return false
	}
	if mountIsNoexec(dir) {
		return false
	}
	f, err := os.CreateTemp(dir, ".wipnote-execprobe-*.sh")
	if err != nil {
		return false
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.WriteString("#!/bin/sh\nexit 0\n"); err != nil {
		_ = f.Close()
		return false
	}
	if err := f.Close(); err != nil {
		return false
	}
	if err := os.Chmod(name, 0o755); err != nil {
		return false
	}
	if err := exec.Command(name).Run(); err != nil {
		return false
	}
	return true
}

func GateTmpRemediation(codeRoot string) string {
	dir := filepath.Join(codeRoot, gateTmpDirName)
	return fmt.Sprintf("mkdir -p %q && TMPDIR=%q GOTMPDIR=%q wipnote check --gate", dir, dir, dir)
}

func IsLikelyNoexecFailure(output string) bool {
	o := strings.ToLower(output)
	if !strings.Contains(o, "permission denied") {
		return false
	}
	return strings.Contains(o, "/tmp/") || strings.Contains(o, "go-build") || strings.Contains(o, "fork/exec") || strings.Contains(o, "exec format") || strings.Contains(o, "text file busy")
}

func RunManagedGate(ctx context.Context, name, dir string, env []string, stdout, stderr io.Writer, argv ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	fmt.Fprintf(stderr, "running: %s (%s)\n", name, strings.Join(argv, " "))
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var combined strings.Builder
	cmd.Stdout = io.MultiWriter(stdout, &combined)
	cmd.Stderr = io.MultiWriter(stderr, &combined)
	if err := cmd.Start(); err != nil {
		return combined.String(), err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			killProcessGroup(cmd)
		case <-done:
		}
	}()
	err := cmd.Wait()
	close(done)
	return combined.String(), err
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pgid, err := syscall.Getpgid(pid); err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = cmd.Process.Kill()
}

func LookupEnv(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):]
		}
	}
	return ""
}

func UpsertEnv(env []string, key, val string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			out = append(out, prefix+val)
			replaced = true
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, prefix+val)
	}
	return out
}
