module aliang.one/nursorgate

go 1.25.1

replace github.com/elazarl/goproxy v1.7.2 => ../goproxy

require (
	github.com/Microsoft/go-winio v0.6.2
	github.com/UserExistsError/conpty v0.1.4
	github.com/creack/pty v1.1.24
	github.com/docker/go-units v0.5.0
	github.com/gen2brain/beeep v0.11.2
	github.com/getkin/kin-openapi v0.134.0
	github.com/getlantern/systray v1.2.2
	github.com/getsentry/sentry-go v0.40.0
	github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510
	github.com/google/uuid v1.6.0
	github.com/gorilla/schema v1.4.1
	github.com/gorilla/websocket v1.5.3
	github.com/miekg/dns v1.1.68
	github.com/oschwald/geoip2-golang v1.9.0
	github.com/sagernet/gvisor v0.0.0-20250325023245-7a9c0f5725fb
	github.com/sagernet/sing v0.7.13
	github.com/sagernet/sing-box v1.12.12
	github.com/sagernet/sing-vmess v0.2.7
	github.com/shirou/gopsutil/v4 v4.26.5
	github.com/spf13/cobra v1.10.1
	github.com/stretchr/testify v1.11.1
	github.com/swaggo/http-swagger/v2 v2.0.2
	github.com/xjasonlyu/tun2socks/v2 v2.6.0
	go.uber.org/automaxprocs v1.6.0
	golang.org/x/time v0.14.0
	golang.zx2c4.com/wireguard v0.0.0-20250521234502-f333402bd9cb
	gopkg.in/natefinch/lumberjack.v2 v2.2.1
	gopkg.in/yaml.v3 v3.0.1
	gorm.io/driver/mysql v1.6.0
	gorm.io/driver/sqlite v1.6.0
	gorm.io/gorm v1.30.0
)

require (
	git.sr.ht/~jackmordaunt/go-toast v1.1.2 // indirect
	github.com/KyleBanks/depth v1.2.1 // indirect
	github.com/andybalholm/brotli v1.1.0 // indirect
	github.com/caddyserver/certmagic v0.23.0 // indirect
	github.com/caddyserver/zerossl v0.1.3 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/ebitengine/purego v0.10.0 // indirect
	github.com/esiqveland/notify v0.13.3 // indirect
	github.com/fsnotify/fsnotify v1.7.0 // indirect
	github.com/getlantern/context v0.0.0-20190109183933-c447772a6520 // indirect
	github.com/getlantern/errors v0.0.0-20190325191628-abdb3e3e36f7 // indirect
	github.com/getlantern/golog v0.0.0-20190830074920-4ef2e798c2d7 // indirect
	github.com/getlantern/hex v0.0.0-20190417191902-c6586a6fe0b7 // indirect
	github.com/getlantern/hidden v0.0.0-20190325191715-f02dbb02be55 // indirect
	github.com/getlantern/ops v0.0.0-20190325191751-d70cb0d6f85f // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/go-openapi/jsonpointer v0.21.0 // indirect
	github.com/go-openapi/jsonreference v0.20.0 // indirect
	github.com/go-openapi/spec v0.20.6 // indirect
	github.com/go-openapi/swag v0.23.0 // indirect
	github.com/go-stack/stack v1.8.0 // indirect
	github.com/godbus/dbus/v5 v5.1.1-0.20230522191255-76236955d466 // indirect
	github.com/gofrs/uuid/v5 v5.3.2 // indirect
	github.com/google/btree v1.1.3 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/jackmordaunt/icns/v3 v3.0.1 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/klauspost/compress v1.17.11 // indirect
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	github.com/libdns/alidns v1.0.5-libdns.v1.beta1 // indirect
	github.com/libdns/cloudflare v0.2.2-0.20250708034226-c574dccb31a6 // indirect
	github.com/libdns/libdns v1.1.0 // indirect
	github.com/logrusorgru/aurora v2.0.3+incompatible // indirect
	github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/mattn/go-sqlite3 v1.14.22 // indirect
	github.com/mdlayher/netlink v1.7.3-0.20250113171957-fbb4dce95f42 // indirect
	github.com/mdlayher/socket v0.5.1 // indirect
	github.com/metacubex/tfo-go v0.0.0-20250921095601-b102db4216c0 // indirect
	github.com/metacubex/utls v1.8.3 // indirect
	github.com/mholt/acmez/v3 v3.1.2 // indirect
	github.com/mohae/deepcopy v0.0.0-20170929034955-c48cc78d4826 // indirect
	github.com/nfnt/resize v0.0.0-20180221191011-83c6a9932646 // indirect
	github.com/oasdiff/yaml v0.0.0-20260313112342-a3ea61cb4d4c // indirect
	github.com/oasdiff/yaml3 v0.0.0-20260224194419-61cd415a242b // indirect
	github.com/oschwald/maxminddb-golang v1.13.1 // indirect
	github.com/oxtoacart/bpool v0.0.0-20190530202638-03653db5a59c // indirect
	github.com/perimeterx/marshmallow v1.1.5 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/power-devops/perfstat v0.0.0-20240221224432-82ca36839d55 // indirect
	github.com/sagernet/fswatch v0.1.1 // indirect
	github.com/sagernet/netlink v0.0.0-20240612041022-b9a21c07ac6a // indirect
	github.com/sagernet/nftables v0.3.0-beta.4 // indirect
	github.com/sagernet/sing-tun v0.7.3 // indirect
	github.com/sergeymakinen/go-bmp v1.0.0 // indirect
	github.com/sergeymakinen/go-ico v1.0.0-beta.0 // indirect
	github.com/swaggo/files/v2 v2.0.0 // indirect
	github.com/swaggo/swag v1.8.1 // indirect
	github.com/tadvi/systray v0.0.0-20190226123456-11a2b8fa57af // indirect
	github.com/tklauser/go-sysconf v0.3.16 // indirect
	github.com/tklauser/numcpus v0.11.0 // indirect
	github.com/vishvananda/netns v0.0.5 // indirect
	github.com/woodsbury/decimal128 v1.3.0 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	github.com/zeebo/blake3 v0.2.4 // indirect
	go.uber.org/zap/exp v0.3.0 // indirect
	go4.org/netipx v0.0.0-20231129151722-fdeea329fbba // indirect
	golang.org/x/crypto v0.44.0 // indirect
	golang.org/x/exp v0.0.0-20250506013437-ce4c2cf36ca6 // indirect
	golang.org/x/mod v0.29.0 // indirect
	golang.org/x/tools v0.38.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
)

require (
	filippo.io/edwards25519 v1.1.0 // indirect
	github.com/go-sql-driver/mysql v1.8.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	go.uber.org/atomic v1.11.0
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.27.0 // indirect
	golang.org/x/net v0.47.0
	golang.org/x/sync v0.18.0
	golang.org/x/sys v0.41.0
	golang.org/x/text v0.31.0
)
