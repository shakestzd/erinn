module github.com/shakestzd/wipnote

go 1.24.0

toolchain go1.24.7

require (
	github.com/PuerkitoBio/goquery v1.10.3
	github.com/gofrs/flock v0.13.0
	github.com/google/uuid v1.6.0
	github.com/microcosm-cc/bluemonday v1.0.27
	github.com/pelletier/go-toml/v2 v2.3.0
	github.com/spf13/cobra v1.9.1
	github.com/tidwall/gjson v1.18.0
	github.com/yuin/goldmark v1.8.2
	go.opentelemetry.io/proto/otlp v1.10.0
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.37.1
)

require golang.org/x/sys v0.41.0 // indirect

require (
	github.com/andybalholm/cascadia v1.3.3 // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/shakestzd/wipnote/core v0.0.0
	github.com/spf13/pflag v1.0.6 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	golang.org/x/exp v0.0.0-20250408133849-7e4ce0ab07d0 // indirect
	golang.org/x/net v0.50.0 // indirect
	modernc.org/libc v1.65.7 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

replace github.com/shakestzd/wipnote/core => ./core
