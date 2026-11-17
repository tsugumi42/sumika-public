module sumika-native

go 1.27.3

tool github.com/egoist/mygo/cmd/mygo

require (
	codeberg.org/readeck/go-readability/v2 v2.1.3
	github.com/6tail/lunar-go v1.4.7
	github.com/ELY-Open-Source/CrychicUI v0.0.0-20261110123803-1c55e6a884bb
	github.com/JohannesKaufmann/html-to-markdown v1.6.0
	github.com/Microsoft/go-winio v0.6.4
	github.com/coder/websocket v1.8.15
	github.com/egoist/mygo v0.9.1
	github.com/gen2brain/pngn v0.1.3
	github.com/gen2brain/vpx v0.2.2
	github.com/goccy/go-yaml v1.19.2
	github.com/google/jsonschema-go v0.4.3
	github.com/klippa-app/go-pdfium v1.21.4
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.com/oklog/ulid/v2 v2.1.2
	github.com/pressly/goose/v3 v3.30.1
	github.com/tetratelabs/wazero v1.12.0
	github.com/tsugumi42/MewTypeUI v0.0.0
	github.com/younland/onnxruntime v0.0.0-20261030082323-8fa9dc98ecfe
	golang.org/x/crypto v0.59.0
	golang.org/x/image v0.48.0
	golang.org/x/net v0.62.0
	golang.org/x/sys v0.50.0
	golang.org/x/text v0.44.0
	modernc.org/sqlite v1.62.0
)

require (
	github.com/ProtonMail/go-crypto v1.4.1 // indirect
	github.com/PuerkitoBio/goquery v1.9.2 // indirect
	github.com/andybalholm/cascadia v1.3.3 // indirect
	github.com/cloudflare/circl v1.6.2 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/go-shiori/dom v0.0.0-20230515143342-73569d674e1c // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	github.com/gogs/chardet v0.0.0-20211120154057-b7413eaefb8f // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/itlightning/dateparse v0.2.1 // indirect
	github.com/jolestar/go-commons-pool/v2 v2.1.2 // indirect
	github.com/klauspost/compress v1.20.4 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mfridman/interpolate v0.2.0 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/sethvargo/go-retry v0.5.0 // indirect
	github.com/ulikunitz/xz v0.5.15 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.digitalxero.dev/rpm v0.3.4 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	modernc.org/libc v1.80.2 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

// MewTypeUI 是私有仓库、还没发版本，源码随仓库放在 third_party/MewTypeUI（来源和更新办法见那里的 来源.md）。
// replace 的目标目录必须在仓库里且已被 git 跟踪，不许本机绝对路径或跳出仓库的路径：internal/archcheck 会查；发版本后改成正式版本、删掉这行 replace
replace github.com/tsugumi42/MewTypeUI => ./third_party/MewTypeUI
