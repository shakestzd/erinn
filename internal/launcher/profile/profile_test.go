package profile_test

import (
	"testing"

	"github.com/shakestzd/wipnote/internal/launcher/mode"
	"github.com/shakestzd/wipnote/internal/launcher/profile"
)

// TestLauncherProfiles validates per-profile defaults for all four deployment
// profiles: host production, devcontainer development, local plugin dev, CI/test.
func TestLauncherProfiles(t *testing.T) {
	tests := []struct {
		name              string
		runtime           mode.RuntimeMode
		pluginMode        mode.PluginMode
		wantDashHost      string
		wantDashPort      int
		wantRolloutMode   profile.RolloutMode
		wantCleanup       profile.CleanupPolicy
		wantCacheLocation profile.CacheLocation
	}{
		{
			name:              "host production — warn-only staged rollout",
			runtime:           mode.RuntimeHost,
			pluginMode:        mode.PluginInstalled,
			wantDashHost:      "127.0.0.1",
			wantDashPort:      8080,
			wantRolloutMode:   profile.RolloutWarnOnly,
			wantCleanup:       profile.CleanupManual,
			wantCacheLocation: profile.CacheUser,
		},
		{
			name:              "devcontainer development — config-gated opt-in",
			runtime:           mode.RuntimeDevcontainer,
			pluginMode:        mode.PluginLocalDev,
			wantDashHost:      "0.0.0.0",
			wantDashPort:      8088,
			wantRolloutMode:   profile.RolloutConfigGated,
			wantCleanup:       profile.CleanupAutoWorktree,
			wantCacheLocation: profile.CacheLocal,
		},
		{
			name:              "local plugin dev — generated port",
			runtime:           mode.RuntimeHost,
			pluginMode:        mode.PluginGeneratedPort,
			wantDashHost:      "127.0.0.1",
			wantDashPort:      8080,
			wantRolloutMode:   profile.RolloutWarnOnly,
			wantCleanup:       profile.CleanupManual,
			wantCacheLocation: profile.CacheUser,
		},
		{
			name:              "CI/test — localhost, config-gated",
			runtime:           mode.RuntimeCI,
			pluginMode:        mode.PluginInstalled,
			wantDashHost:      "127.0.0.1",
			wantDashPort:      8080,
			wantRolloutMode:   profile.RolloutConfigGated,
			wantCleanup:       profile.CleanupAutoWorktree,
			wantCacheLocation: profile.CacheLocal,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := profile.ForRuntime(tc.runtime, tc.pluginMode)

			if p.DashboardHost != tc.wantDashHost {
				t.Errorf("DashboardHost: got %q, want %q", p.DashboardHost, tc.wantDashHost)
			}
			if p.DashboardPort != tc.wantDashPort {
				t.Errorf("DashboardPort: got %d, want %d", p.DashboardPort, tc.wantDashPort)
			}
			if p.Rollout != tc.wantRolloutMode {
				t.Errorf("Rollout: got %v, want %v", p.Rollout, tc.wantRolloutMode)
			}
			if p.Cleanup != tc.wantCleanup {
				t.Errorf("Cleanup: got %v, want %v", p.Cleanup, tc.wantCleanup)
			}
			if p.Cache != tc.wantCacheLocation {
				t.Errorf("Cache: got %v, want %v", p.Cache, tc.wantCacheLocation)
			}
		})
	}
}

// TestHostProfileNeverForcesIsolation ensures host profile never flips
// EnforceIsolation on — staged-rollout guarantee.
func TestHostProfileNeverForcesIsolation(t *testing.T) {
	p := profile.ForRuntime(mode.RuntimeHost, mode.PluginInstalled)
	if p.EnforceIsolation {
		t.Error("host profile: EnforceIsolation must be false (staged-rollout — only after slice-9 migration tooling)")
	}
	if p.Rollout != profile.RolloutWarnOnly {
		t.Errorf("host profile: Rollout must be RolloutWarnOnly, got %v", p.Rollout)
	}
}

// TestDevcontainerBindDefault verifies 0.0.0.0:8088 for devcontainer, consistent
// with plan-ae0c37b2 slice-4 and bug-3a373884 forwardPorts convention.
func TestDevcontainerBindDefault(t *testing.T) {
	p := profile.ForRuntime(mode.RuntimeDevcontainer, mode.PluginLocalDev)
	if p.DashboardHost != "0.0.0.0" {
		t.Errorf("devcontainer DashboardHost: got %q, want 0.0.0.0", p.DashboardHost)
	}
	if p.DashboardPort != 8088 {
		t.Errorf("devcontainer DashboardPort: got %d, want 8088", p.DashboardPort)
	}
}

// TestProfileLabel verifies each profile produces a human-readable label.
func TestProfileLabel(t *testing.T) {
	cases := []struct {
		runtime    mode.RuntimeMode
		pluginMode mode.PluginMode
		wantLabel  string
	}{
		{mode.RuntimeHost, mode.PluginInstalled, "host-production"},
		{mode.RuntimeDevcontainer, mode.PluginLocalDev, "devcontainer-dev"},
		{mode.RuntimeHost, mode.PluginGeneratedPort, "local-plugin-dev"},
		{mode.RuntimeCI, mode.PluginInstalled, "ci-test"},
	}
	for _, tc := range cases {
		p := profile.ForRuntime(tc.runtime, tc.pluginMode)
		if p.Label != tc.wantLabel {
			t.Errorf("Label: got %q, want %q", p.Label, tc.wantLabel)
		}
	}
}
