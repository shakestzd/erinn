module github.com/shakestzd/wipnote/observe

go 1.25.0

require (
	github.com/shakestzd/wipnote/core v0.0.0
	github.com/shakestzd/wipnote/port v0.0.0
	go.opentelemetry.io/proto/otlp v1.10.0
	golang.org/x/sys v0.47.0
	google.golang.org/protobuf v1.36.11
	modernc.org/sqlite v1.56.0
)

require (
	github.com/PuerkitoBio/goquery v1.10.3 // indirect
	github.com/andybalholm/cascadia v1.3.3 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/pelletier/go-toml/v2 v2.3.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	golang.org/x/net v0.50.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

// Interim multi-module wiring: observe/ is the dashboard + OTEL observability
// product carved out of the monolith (trk-505c1685, final phase of the
// trk-1f94484c modular carve-out). It imports core/ (db, hooks, harness,
// eventsink) and port/ (pluginbuild, for the port-drift lifecycle hook).
// Same local-replace rationale as core/, plan/, and port/ in the root go.mod;
// swap these replaces for real version requires when the modules are tagged
// and released.
replace github.com/shakestzd/wipnote/core => ../core

replace github.com/shakestzd/wipnote/port => ../port
