package main

import (
	"context"
	"io"

	"github.com/shakestzd/wipnote/internal/gate"
)

const gateTmpDirName = ".test-tmp"

func gateExecEnv(codeRoot string) (env []string, redirected bool, tmpDir string) {
	return gate.GateExecEnv(codeRoot)
}

func effectiveTmpDir(env []string) string {
	return gate.EffectiveTmpDir(env)
}

func dirIsExecCapable(dir string) bool {
	return gate.DirIsExecCapable(dir)
}

func isLikelyNoexecFailure(output string) bool {
	return gate.IsLikelyNoexecFailure(output)
}

func gateTmpRemediation(codeRoot string) string {
	return gate.GateTmpRemediation(codeRoot)
}

func runManagedGate(ctx context.Context, name, dir string, env []string, stdout, stderr io.Writer, argv ...string) (string, error) {
	return gate.RunManagedGate(ctx, name, dir, env, stdout, stderr, argv...)
}

func lookupEnv(env []string, key string) string {
	return gate.LookupEnv(env, key)
}

func upsertEnv(env []string, key, val string) []string {
	return gate.UpsertEnv(env, key, val)
}
