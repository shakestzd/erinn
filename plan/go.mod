module github.com/shakestzd/wipnote/plan

go 1.25.0

require (
	github.com/dop251/goja v0.0.0-20250630131328-58d95d85e994
	github.com/microcosm-cc/bluemonday v1.0.27
	github.com/shakestzd/wipnote/core v0.0.0
	github.com/yuin/goldmark v1.8.2
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/PuerkitoBio/goquery v1.10.3 // indirect
	github.com/andybalholm/cascadia v1.3.3 // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/dlclark/regexp2 v1.11.4 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-sourcemap/sourcemap v2.1.3+incompatible // indirect
	github.com/google/pprof v0.0.0-20260802141513-ef3492d7dac3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/net v0.50.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.56.0 // indirect
)

// Interim multi-module wiring: plan/ is part of this monorepo and depends on the
// sibling core/ module, which is likewise not yet published/tagged. It is consumed
// via a local replace mirroring the core arrangement (see root go.mod). When core
// and plan are tagged and released, swap these replaces for real version requires.
replace github.com/shakestzd/wipnote/core => ../core
