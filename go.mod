module github.com/hanzoai/cloud

go 1.26.8

// Dependencies will be added as subsystems are mounted per HIP-0106.

require (
	github.com/dop251/goja v0.0.0-20260627200808-0b76000cabdb
	github.com/hanzoai/goa v1.0.0
	github.com/hanzoai/s3-go v1.0.0
	github.com/hanzoai/sqlite v0.3.2
	github.com/lib/pq v1.12.3
	github.com/luxfi/log v1.5.0
	github.com/luxfi/zapdb v1.10.1
	github.com/vulcand/oxy/v2 v2.0.0-00010101000000-000000000000
	github.com/zap-proto/fiber/v3 v3.2.1
	github.com/zap-proto/go v1.3.0
	github.com/zap-proto/md v0.1.0
	github.com/zap-proto/zip v1.18.22
)

require (
	github.com/hanzoai/csqlite v0.1.0 // indirect
	github.com/hanzoai/sqlcipher v0.1.0 // indirect
	github.com/hanzos3/go-sdk v1.0.2 // indirect
	github.com/zap-proto/http v0.3.1 // indirect
)

require (
	github.com/HdrHistogram/hdrhistogram-go v1.2.0 // indirect
	github.com/google/flatbuffers v25.12.19+incompatible // indirect
	github.com/segmentio/fasthash v1.0.3 // indirect
)

require (
	filippo.io/hpke v0.4.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff v2.2.1+incompatible // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cloudflare/circl v1.6.3 // indirect
	github.com/dgraph-io/ristretto/v2 v2.4.0 // indirect
	github.com/dlclark/regexp2/v2 v2.2.1 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/evanw/esbuild v0.28.1 // indirect
	github.com/fasthttp/websocket v1.5.12 // indirect
	github.com/go-ini/ini v1.67.0 // indirect
	github.com/go-jose/go-jose/v4 v4.1.4
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-python/gpython v0.2.0
	github.com/go-sourcemap/sourcemap v2.1.4+incompatible // indirect
	github.com/google/pprof v0.0.0-20260402051712-545e8a4df936 // indirect
	github.com/gorilla/websocket v1.5.4-0.20250319132907-e064f32e3674 // indirect
	github.com/grandcat/zeroconf v1.0.0 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/klauspost/crc32 v1.3.0 // indirect
	github.com/luxfi/accel v1.2.4 // indirect
	github.com/luxfi/age v1.6.0 // indirect
	github.com/luxfi/crypto v1.20.2 // indirect
	github.com/luxfi/kms v1.11.8
	github.com/luxfi/mdns v0.1.1 // indirect
	github.com/luxfi/pq v1.1.0 // indirect
	github.com/luxfi/zap v1.2.6 // indirect
	github.com/miekg/dns v1.1.72 // indirect
	github.com/minio/crc64nvme v1.1.1 // indirect
	github.com/minio/md5-simd v1.1.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/prometheus/client_golang v1.23.2
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.67.5 // indirect
	github.com/prometheus/procfs v0.20.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/savsgio/gotils v0.0.0-20240704082632-aef3928b8a38 // indirect
	github.com/tetratelabs/wazero v1.12.0
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.44.0
	go.opentelemetry.io/otel/metric v1.44.0 // indirect
	go.opentelemetry.io/otel/trace v1.44.0
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/tools v0.47.0 // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
)

require (
	github.com/andybalholm/brotli v1.2.1 // indirect
	github.com/gofiber/schema v1.7.1 // indirect
	github.com/gofiber/utils/v2 v2.0.4 // indirect
	github.com/google/uuid v1.6.1-0.20241114170450-2d3c2a9cc518 // indirect
	github.com/klauspost/compress v1.18.6 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.22 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasthttp v1.72.0
	golang.org/x/crypto v0.54.0
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
)

replace github.com/apache/thrift => github.com/apache/thrift v0.16.0

replace github.com/uber/jaeger-client-go => github.com/uber/jaeger-client-go v2.28.0+incompatible

replace github.com/abbot/go-http-auth => github.com/containous/go-http-auth v0.4.1-0.20200324110947-a37a7636d23e

replace github.com/mailgun/minheap => github.com/containous/minheap v0.0.0-20190809180810-6e71eb837595

replace github.com/vulcand/oxy/v2 => github.com/traefik/oxy/v2 v2.0.0-20260126093803-fb11d60e0fdf

// Phantom-version fix: some transitive dep requires the non-existent
// release (drop-in, package sqlite3) via a VERSIONED replace (left side pins the
// phantom) so Go never tries to read v2.0.3's go.mod during module-graph load —
// modernc). Every cloud store imports the fork, never modernc directly, so the
// "sqlite" driver is registered exactly once.

exclude github.com/ugorji/go v0.0.0-20171122102828-84cb69a8af83
