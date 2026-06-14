package launcher

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/shakestzd/wipnote/core/harness"
)

type HarnessResult struct {
	Err        error
	ReraiseSig syscall.Signal
	ExitCode   int
}

func AppendOrReplaceEnv(env []string, kv ...string) []string {
	for _, pair := range kv {
		key := pair
		if idx := strings.IndexByte(pair, '='); idx >= 0 {
			key = pair[:idx+1]
		}
		replaced := false
		for i, existing := range env {
			if strings.HasPrefix(existing, key) {
				env[i] = pair
				replaced = true
				break
			}
		}
		if !replaced {
			env = append(env, pair)
		}
	}
	return env
}

func BuildHarnessOtelEnv(base []string, harnessID string, port int, sessionID string) []string {
	if port == 0 {
		return base
	}
	cfg := harness.Get(harnessID)
	if cfg == nil || cfg.OtelEnv == nil {
		return base
	}
	env := make([]string, len(base))
	copy(env, base)
	return AppendOrReplaceEnv(env, cfg.OtelEnv(port, sessionID)...)
}

func BuildHarnessAgentEnv(base []string, harnessID string) []string {
	cfg := harness.Get(harnessID)
	if cfg == nil {
		return base
	}
	env := make([]string, len(base))
	copy(env, base)
	return AppendOrReplaceEnv(env, cfg.BuildAgentEnv()...)
}

func RunHarnessWithCleanupCore(c *exec.Cmd, cleanup func()) HarnessResult {
	var once sync.Once
	callCleanup := func() {
		once.Do(func() {
			if cleanup != nil {
				cleanup()
			}
		})
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	if err := c.Start(); err != nil {
		callCleanup()
		return HarnessResult{Err: fmt.Errorf("start harness: %w", err)}
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- c.Wait() }()

	var sigReceived os.Signal
	select {
	case sigReceived = <-sigCh:
		if c.Process != nil {
			_ = c.Process.Signal(sigReceived)
		}
		<-waitCh
	case <-waitCh:
	}

	callCleanup()

	if sigReceived != nil {
		if sysSig, ok := sigReceived.(syscall.Signal); ok {
			return HarnessResult{ReraiseSig: sysSig}
		}
	}

	if c.ProcessState != nil {
		if ws, ok := c.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return HarnessResult{ReraiseSig: ws.Signal()}
		}
		if !c.ProcessState.Success() {
			return HarnessResult{ExitCode: c.ProcessState.ExitCode()}
		}
	}

	return HarnessResult{}
}

func RunHarnessWithCleanup(c *exec.Cmd, cleanup func()) error {
	res := RunHarnessWithCleanupCore(c, cleanup)
	if res.Err != nil {
		return res.Err
	}
	if res.ReraiseSig != 0 {
		signal.Reset(syscall.SIGINT, syscall.SIGTERM)
		_ = syscall.Kill(os.Getpid(), res.ReraiseSig)
		return nil
	}
	if res.ExitCode != 0 {
		os.Exit(res.ExitCode)
	}
	return nil
}
