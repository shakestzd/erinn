module github.com/shakestzd/wipnote

go 1.25.0

require (
	github.com/PuerkitoBio/goquery v1.10.3
	github.com/charmbracelet/bubbles v0.21.1-0.20250623103423-23b8fd6302d7
	github.com/charmbracelet/huh v1.0.0
	github.com/charmbracelet/lipgloss v1.1.0
	github.com/gofrs/flock v0.13.0
	github.com/google/uuid v1.6.0
	github.com/lucasb-eyer/go-colorful v1.2.0
	github.com/muesli/termenv v0.16.0
	github.com/pelletier/go-toml/v2 v2.3.0
	github.com/spf13/cobra v1.9.1
	github.com/tidwall/gjson v1.18.0
	go.opentelemetry.io/proto/otlp v1.10.0
	golang.org/x/sys v0.47.0
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.56.0
)

require (
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/catppuccin/go v0.3.0 // indirect
	github.com/charmbracelet/bubbletea v1.3.6 // indirect
	github.com/charmbracelet/colorprofile v0.2.3-0.20250311203215-f60798e515dc // indirect
	github.com/charmbracelet/x/ansi v0.9.3 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.13 // indirect
	github.com/charmbracelet/x/exp/strings v0.0.0-20240722160745-212f7b056ed0 // indirect
	github.com/charmbracelet/x/term v0.2.1 // indirect
	github.com/dlclark/regexp2 v1.11.4 // indirect
	github.com/dop251/goja v0.0.0-20250630131328-58d95d85e994 // indirect
	github.com/erikgeiser/coninput v0.0.0-20211004153227-1c3628e74d0f // indirect
	github.com/go-sourcemap/sourcemap v2.1.3+incompatible // indirect
	github.com/google/pprof v0.0.0-20260802141513-ef3492d7dac3 // indirect
	github.com/mattn/go-localereader v0.0.1 // indirect
	github.com/mattn/go-runewidth v0.0.16 // indirect
	github.com/microcosm-cc/bluemonday v1.0.27 // indirect
	github.com/mitchellh/hashstructure/v2 v2.0.2 // indirect
	github.com/muesli/ansi v0.0.0-20230316100256-276c6243b2f6 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	github.com/yuin/goldmark v1.8.2 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/text v0.34.0 // indirect
)

require (
	github.com/andybalholm/cascadia v1.3.3 // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/shakestzd/wipnote/core v0.0.0
	github.com/shakestzd/wipnote/observe v0.0.0
	github.com/shakestzd/wipnote/plan v0.0.0
	github.com/shakestzd/wipnote/port v0.0.0
	github.com/spf13/pflag v1.0.6 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	golang.org/x/net v0.50.0 // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

// Interim multi-module wiring: core/ is part of this monorepo and not yet
// published/tagged, so it is consumed via a local replace. This is portable
// for every supported install path (local `wipnote build` and the Homebrew
// tarball both build from the checkout). `go install <pkg>@version` is not a
// supported install path today; when core is tagged and released, swap this
// replace for a real version require (tracked on trk-03da9cce).
replace github.com/shakestzd/wipnote/core => ./core

// Interim multi-module wiring: plan/ is the planning product carved out of the
// monolith (trk-49c43b06). Same local-replace rationale as core/ above; swap for
// a real version require when plan is tagged and released.
replace github.com/shakestzd/wipnote/plan => ./plan

// Interim multi-module wiring: port/ is the plugin-porting product lifted out of
// the monolith (trk-1ea27426). It houses the pluginbuild generator plus the
// generated Codex/Antigravity target trees. Same local-replace rationale as core/ and
// plan/ above; swap for a real version require when port is tagged and released.
replace github.com/shakestzd/wipnote/port => ./port

// Interim multi-module wiring: observe/ is the dashboard + OTEL observability
// product lifted out of the monolith (trk-505c1685, final phase of trk-1f94484c).
// It houses the OTEL collector/ingest/receiver/sink packages plus pricing and the
// lifecycle-registration shim. Same local-replace rationale as core/, plan/, and
// port/ above; swap for a real version require when observe is tagged and released.
replace github.com/shakestzd/wipnote/observe => ./observe
