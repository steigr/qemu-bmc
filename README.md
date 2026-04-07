# qemu-bmc

A Go single binary that controls QEMU VMs via both Redfish API (HTTP/HTTPS) and IPMI over LAN (UDP).

```
┌──────────────────────────────────────────┐
│ OCI Container                            │
│                                          │
│  ┌───────────┐   ┌──────────────────┐   │
│  │ qemu-bmc  │   │      QEMU        │   │
│  │ (Go)      │   │                  │   │
│  │           │   │                  │   │
│  │ :8080/tcp │   │                  │   │
│  │  Redfish  ├──►│  QMP Socket      │   │
│  │           │   │                  │   │
│  │ :6623/udp │   │                  │   │
│  │  IPMI     ├──►│                  │   │
│  │           │   │                  │   │
│  │ :9002/tcp │   │  ipmi-bmc-extern │   │
│  │  VM IPMI  │◄──┤  (KCS)          │   │
│  └───────────┘   └──────────────────┘   │
└──────────────────────────────────────────┘
```

## Features

- **Redfish API** - ServiceRoot, Systems, Managers, VirtualMedia, Chassis (gofish compatible)
- **IPMI over LAN** - RMCP/RMCP+, RAKP HMAC-SHA1 authentication, AES-CBC-128 encryption
- **VM IPMI (In-Band)** - Guest OS IPMI via QEMU `ipmi-bmc-extern` KCS interface for MaaS commissioning
- **noVNC** - Browser-based VNC console served on the Redfish HTTP port (no extra port needed)
- **QMP Control** - Power operations, boot device changes, VirtualMedia mount
- **Compatibility** - MAAS, Tinkerbell/Rufio

## Quick Start

### Docker Compose (Process Management Mode)

qemu-bmc manages the QEMU process directly inside the container:

```bash
# Create VM disk
mkdir -p vm
qemu-img create -f qcow2 vm/disk.qcow2 20G

# Start
docker compose up -d

# Check status
ipmitool -I lanplus -H localhost -U admin -P password mc info
curl -u admin:password http://127.0.0.1:8080/redfish/v1/Systems/1
```

### Docker Build

```bash
docker build -t qemu-bmc -f docker/Dockerfile .
docker run --rm --device /dev/kvm --cap-add NET_ADMIN \
  -p 5900:5900 -p 6623:6623/udp -p 8080:8080 \
  -v ./vm:/vm \
  -e VM_MEMORY=4096 -e VM_CPUS=4 \
  qemu-bmc
```

### Binary (Legacy Mode)

Connect to an existing QEMU instance via QMP socket:

```bash
go build -o qemu-bmc ./cmd/qemu-bmc

export QMP_SOCK=/var/run/qemu/qmp.sock
export IPMI_USER=admin
export IPMI_PASS=password
./qemu-bmc
```

## Usage

### Redfish

```bash
# Service root
curl -u admin:password http://127.0.0.1:8080/redfish/v1/

# Check power state
curl -u admin:password http://127.0.0.1:8080/redfish/v1/Systems/1

# Power on
curl -u admin:password -X POST \
  -H 'Content-Type: application/json' \
  -d '{"ResetType": "On"}' \
  http://127.0.0.1:8080/redfish/v1/Systems/1/Actions/ComputerSystem.Reset

# Force power off
curl -u admin:password -X POST \
  -H 'Content-Type: application/json' \
  -d '{"ResetType": "ForceOff"}' \
  http://127.0.0.1:8080/redfish/v1/Systems/1/Actions/ComputerSystem.Reset

# Set PXE boot
curl -u admin:password -X PATCH \
  -H 'Content-Type: application/json' \
  -d '{"Boot": {"BootSourceOverrideEnabled": "Once", "BootSourceOverrideTarget": "Pxe"}}' \
  http://127.0.0.1:8080/redfish/v1/Systems/1

# Mount VirtualMedia
curl -u admin:password -X POST \
  -H 'Content-Type: application/json' \
  -d '{"Image": "http://example.com/boot.iso", "Inserted": true}' \
  http://127.0.0.1:8080/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia

# Unmount VirtualMedia
curl -u admin:password -X POST \
  http://127.0.0.1:8080/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia
```

### IPMI

```bash
# Check power state
ipmitool -I lanplus -H localhost -p 6623 -U admin -P password chassis status

# Power on
ipmitool -I lanplus -H localhost -p 6623 -U admin -P password chassis power on

# Power off
ipmitool -I lanplus -H localhost -p 6623 -U admin -P password chassis power off

# Set PXE boot
ipmitool -I lanplus -H localhost -p 6623 -U admin -P password chassis bootdev pxe
```

### noVNC

A browser-based VNC console is available on the same HTTP port as the Redfish API. No extra port or VNC viewer software is required.

Open a browser and navigate to:

```
http://127.0.0.1:8080/novnc/
```

The browser will prompt for Basic Auth (same credentials as Redfish). After authentication, the noVNC UI loads and connects to QEMU's VNC server automatically.

| URL | Description |
|-----|-------------|
| `GET /novnc/` | Redirects to noVNC UI (`/novnc/vnc.html`) |
| `GET /novnc/vnc.html` | noVNC web UI |
| `GET /websockify` | WebSocket-to-VNC proxy (used internally by noVNC) |

The VNC target address is controlled by the `VNC_ADDR` environment variable (default: `localhost:5900`).

### VM IPMI (In-Band)

qemu-bmc supports QEMU's `ipmi-bmc-extern` device for in-band IPMI from the guest OS. This enables MaaS commissioning scripts to configure BMC users, LAN settings, and channel access from within the VM. Users created in-band are automatically available for out-of-band IPMI and Redfish authentication.

Set `VM_IPMI_ADDR` to enable the VM IPMI chardev listener:

```bash
export VM_IPMI_ADDR=:9002
```

Configure QEMU to connect to qemu-bmc:

```bash
qemu-system-x86_64 \
  -chardev socket,id=ipmi0,host=localhost,port=9002,reconnect=10 \
  -device ipmi-bmc-extern,id=bmc0,chardev=ipmi0 \
  -device isa-ipmi-kcs,bmc=bmc0 \
  ...
```

The guest OS can then use standard `ipmitool` commands via the KCS interface:

```bash
# Inside the VM
ipmitool user set name 3 newuser
ipmitool user set password 3 newpass
ipmitool channel setaccess 1 3 callin=on ipmi=on link=on privilege=4
ipmitool user enable 3
```

## Redfish Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/redfish/v1` | ServiceRoot |
| GET | `/redfish/v1/Systems` | System collection |
| GET | `/redfish/v1/Systems/1` | Computer system |
| PATCH | `/redfish/v1/Systems/1` | Boot device override |
| POST | `/redfish/v1/Systems/1/Actions/ComputerSystem.Reset` | Power control |
| GET | `/redfish/v1/Managers` | Manager collection |
| GET | `/redfish/v1/Managers/1` | BMC manager |
| GET | `/redfish/v1/Managers/1/VirtualMedia` | VirtualMedia collection |
| GET | `/redfish/v1/Managers/1/VirtualMedia/CD1` | VirtualMedia resource |
| POST | `.../VirtualMedia.InsertMedia` | Insert media |
| POST | `.../VirtualMedia.EjectMedia` | Eject media |
| GET/HEAD | `/redfish/v1/Managers/1/VirtualMedia/CD1/Proxy?image=<url>` | VirtualMedia proxy with range support |
| GET | `/redfish/v1/Chassis` | Chassis collection |
| GET | `/redfish/v1/Chassis/1` | Chassis resource |
| GET | `/novnc/` | Redirect to noVNC UI |
| GET | `/novnc/vnc.html` | Browser-based VNC console |
| GET | `/websockify` | WebSocket-to-VNC proxy |

## IPMI Commands

| Command | Description |
|---------|-------------|
| Get Device ID | BMC identity |
| Get Channel Auth Capabilities | Auth type negotiation |
| Get Chassis Status | Power state query |
| Chassis Control | Power on/off/cycle/reset |
| Set/Get Boot Options | Boot device override |

## Environment Variables

### BMC Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `QMP_SOCK` | `/var/run/qemu/qmp.sock` | QMP socket path |
| `IPMI_USER` | `admin` | Authentication username |
| `IPMI_PASS` | `password` | Authentication password |
| `REDFISH_ADDR` | `127.0.0.1` | Redfish bind address |
| `REDFISH_PORT` | `8080` | Redfish HTTP port |
| `IPMI_PORT` | `6623` | IPMI UDP port |
| `SERIAL_ADDR` | `localhost:9002` | SOL bridge target |
| `TLS_CERT` | (empty) | TLS certificate path (TLS is enabled only when both `TLS_CERT` and `TLS_KEY` are set) |
| `TLS_KEY` | (empty) | TLS private key path |
| `VM_BOOT_MODE` | `bios` | Default boot mode (`bios` or `uefi`) |
| `VM_IPMI_ADDR` | (empty, disabled) | VM IPMI chardev listen address (e.g., `:9002`) |
| `VNC_ADDR` | `localhost:5900` | QEMU VNC TCP address for noVNC proxy |
| `POWER_ON_AT_START` | `false` | Power on VM automatically at startup (useful for non-MAAS setups) |

### Container Configuration

These variables are used by `docker/entrypoint.sh` to construct QEMU arguments:

| Variable | Default | Description |
|----------|---------|-------------|
| `VM_MEMORY` | `2048` | VM memory in MB |
| `VM_CPUS` | `2` | Number of VM CPUs |
| `ENABLE_KVM` | `true` | Use KVM acceleration (falls back to TCG) |
| `VNC_PORT` | `5900` | VNC display port |
| `VM_DISK` | `/vm/disk.qcow2` | VM disk image path |
| `VM_CDROM` | (empty) | CD-ROM ISO path |
| `VM_BOOT` | `c` | Boot device (c=disk, d=cdrom, n=network) |
| `VM_BOOT_MENU_TIMEOUT` | `0` | Boot menu timeout in ms (0=disabled) |
| `VM_NETWORKS` | (empty) | Comma-separated host interfaces for TAP passthrough (waits up to 30s for interfaces to appear, useful for containerlab) |
| `QEMU_EXTRA_ARGS` | (empty) | Additional QEMU arguments |
| `DEBUG` | `false` | Enable debug output |

## Development

```bash
# Build
go build ./cmd/qemu-bmc
make docker-build

# Unit tests
go test ./... -count=1
go test ./... -race -count=1

# Static analysis
go vet ./...

# Container integration tests
make container-test         # Quick smoke tests
make container-test-all     # All 82 tests (9 categories)
```

## Architecture

```
cmd/qemu-bmc/main.go          # Entrypoint
internal/
  qmp/                         # QMP socket client
  qemu/                        # QEMU process management
  machine/                     # VM state management
  redfish/                     # Redfish HTTP server (gorilla/mux)
  ipmi/                        # IPMI UDP server + VM chardev server (RMCP/RMCP+)
  novnc/                       # noVNC static files (embedded) + WebSocket-to-VNC proxy
  bmc/                         # BMC configuration state (users, LAN, channels)
  config/                      # Environment variable config
docker/
  Dockerfile                   # Multi-stage build (Go builder + Debian runtime)
  entrypoint.sh                # Environment variables → QEMU args
  setup-network.sh             # TAP/bridge network setup
tests/                         # Bash-based container integration tests
containerlab/                  # containerlab topology example
```

## License

TBD

---

# qemu-bmc (日本語)

QEMU VM を Redfish API (HTTP/HTTPS) と IPMI over LAN (UDP) の両方で制御する Go シングルバイナリ。

```
┌──────────────────────────────────────────┐
│ OCI コンテナ                              │
│                                          │
│  ┌───────────┐   ┌──────────────────┐   │
│  │ qemu-bmc  │   │      QEMU        │   │
│  │ (Go)      │   │                  │   │
│  │           │   │                  │   │
│  │ :8080/tcp │   │                  │   │
│  │  Redfish  ├──►│  QMP Socket      │   │
│  │           │   │                  │   │
│  │ :6623/udp │   │                  │   │
│  │  IPMI     ├──►│                  │   │
│  │           │   │                  │   │
│  │ :9002/tcp │   │  ipmi-bmc-extern │   │
│  │  VM IPMI  │◄──┤  (KCS)          │   │
│  └───────────┘   └──────────────────┘   │
└──────────────────────────────────────────┘
```

## 機能

- **Redfish API** - ServiceRoot, Systems, Managers, VirtualMedia, Chassis (gofish 互換)
- **IPMI over LAN** - RMCP/RMCP+, RAKP HMAC-SHA1 認証, AES-CBC-128 暗号化
- **VM IPMI（イン・バンド）** - QEMU `ipmi-bmc-extern` KCS インターフェースによるゲスト OS IPMI（MaaS コミッショニング対応）
- **noVNC** - Redfish HTTP ポートでブラウザから VNC コンソールにアクセス（追加ポート不要）
- **QMP 制御** - 電源操作、ブートデバイス変更、VirtualMedia マウント
- **互換性** - MAAS, Tinkerbell/Rufio

## クイックスタート

### Docker Compose（プロセス管理モード）

qemu-bmc がコンテナ内で QEMU プロセスを直接管理します:

```bash
# VM ディスク作成
mkdir -p vm
qemu-img create -f qcow2 vm/disk.qcow2 20G

# 起動
docker compose up -d

# 状態確認
ipmitool -I lanplus -H localhost -U admin -P password mc info
curl -u admin:password http://127.0.0.1:8080/redfish/v1/Systems/1
```

### Docker ビルド

```bash
docker build -t qemu-bmc -f docker/Dockerfile .
docker run --rm --device /dev/kvm --cap-add NET_ADMIN \
  -p 5900:5900 -p 6623:6623/udp -p 8080:8080 \
  -v ./vm:/vm \
  -e VM_MEMORY=4096 -e VM_CPUS=4 \
  qemu-bmc
```

### バイナリ（レガシーモード）

既存の QEMU インスタンスに QMP ソケット経由で接続:

```bash
go build -o qemu-bmc ./cmd/qemu-bmc

export QMP_SOCK=/var/run/qemu/qmp.sock
export IPMI_USER=admin
export IPMI_PASS=password
./qemu-bmc
```

## 使い方

### Redfish

```bash
# サービスルート
curl -u admin:password http://127.0.0.1:8080/redfish/v1/

# 電源状態の確認
curl -u admin:password http://127.0.0.1:8080/redfish/v1/Systems/1

# 電源オン
curl -u admin:password -X POST \
  -H 'Content-Type: application/json' \
  -d '{"ResetType": "On"}' \
  http://127.0.0.1:8080/redfish/v1/Systems/1/Actions/ComputerSystem.Reset

# 電源オフ（強制）
curl -u admin:password -X POST \
  -H 'Content-Type: application/json' \
  -d '{"ResetType": "ForceOff"}' \
  http://127.0.0.1:8080/redfish/v1/Systems/1/Actions/ComputerSystem.Reset

# PXE ブート設定
curl -u admin:password -X PATCH \
  -H 'Content-Type: application/json' \
  -d '{"Boot": {"BootSourceOverrideEnabled": "Once", "BootSourceOverrideTarget": "Pxe"}}' \
  http://127.0.0.1:8080/redfish/v1/Systems/1

# VirtualMedia マウント
curl -u admin:password -X POST \
  -H 'Content-Type: application/json' \
  -d '{"Image": "http://example.com/boot.iso", "Inserted": true}' \
  http://127.0.0.1:8080/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia

# VirtualMedia アンマウント
curl -u admin:password -X POST \
  http://127.0.0.1:8080/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia
```

### IPMI

```bash
# 電源状態の確認
ipmitool -I lanplus -H localhost -p 6623 -U admin -P password chassis status

# 電源オン
ipmitool -I lanplus -H localhost -p 6623 -U admin -P password chassis power on

# 電源オフ
ipmitool -I lanplus -H localhost -p 6623 -U admin -P password chassis power off

# PXE ブート設定
ipmitool -I lanplus -H localhost -p 6623 -U admin -P password chassis bootdev pxe
```

### noVNC

Redfish API と同じ HTTP ポートでブラウザベースの VNC コンソールを利用できます。追加ポートや VNC ビューアソフトウェアは不要です。

ブラウザで以下の URL を開きます:

```
http://127.0.0.1:8080/novnc/
```

Basic 認証のプロンプトが表示されます（Redfish と同じ認証情報）。認証後、noVNC UI が表示され、QEMU の VNC サーバーに自動的に接続します。

| URL | 説明 |
|-----|------|
| `GET /novnc/` | noVNC UI へリダイレクト (`/novnc/vnc.html`) |
| `GET /novnc/vnc.html` | noVNC Web UI |
| `GET /websockify` | WebSocket-to-VNC プロキシ（noVNC が内部で使用） |

VNC の接続先アドレスは `VNC_ADDR` 環境変数で制御します（デフォルト: `localhost:5900`）。

### VM IPMI（イン・バンド）

qemu-bmc は QEMU の `ipmi-bmc-extern` デバイスを使ったゲスト OS からのイン・バンド IPMI をサポートします。MaaS コミッショニングスクリプトが VM 内から BMC ユーザー、LAN 設定、チャネルアクセスを設定できます。イン・バンドで作成されたユーザーは、アウト・オブ・バンド IPMI および Redfish 認証でも自動的に利用可能です。

`VM_IPMI_ADDR` を設定して VM IPMI chardev リスナーを有効にします:

```bash
export VM_IPMI_ADDR=:9002
```

QEMU を qemu-bmc に接続するよう設定します:

```bash
qemu-system-x86_64 \
  -chardev socket,id=ipmi0,host=localhost,port=9002,reconnect=10 \
  -device ipmi-bmc-extern,id=bmc0,chardev=ipmi0 \
  -device isa-ipmi-kcs,bmc=bmc0 \
  ...
```

ゲスト OS 内で標準の `ipmitool` コマンドが KCS インターフェース経由で使用できます:

```bash
# VM 内
ipmitool user set name 3 newuser
ipmitool user set password 3 newpass
ipmitool channel setaccess 1 3 callin=on ipmi=on link=on privilege=4
ipmitool user enable 3
```

## Redfish エンドポイント

| メソッド | パス | 説明 |
|---------|------|------|
| GET | `/redfish/v1` | サービスルート |
| GET | `/redfish/v1/Systems` | システムコレクション |
| GET | `/redfish/v1/Systems/1` | コンピュータシステム |
| PATCH | `/redfish/v1/Systems/1` | ブートデバイス変更 |
| POST | `/redfish/v1/Systems/1/Actions/ComputerSystem.Reset` | 電源制御 |
| GET | `/redfish/v1/Managers` | マネージャコレクション |
| GET | `/redfish/v1/Managers/1` | BMC マネージャ |
| GET | `/redfish/v1/Managers/1/VirtualMedia` | VirtualMedia コレクション |
| GET | `/redfish/v1/Managers/1/VirtualMedia/CD1` | VirtualMedia リソース |
| POST | `.../VirtualMedia.InsertMedia` | メディア挿入 |
| POST | `.../VirtualMedia.EjectMedia` | メディア取り出し |
| GET/HEAD | `/redfish/v1/Managers/1/VirtualMedia/CD1/Proxy?image=<url>` | Range 対応 VirtualMedia プロキシ |
| GET | `/redfish/v1/Chassis` | シャーシコレクション |
| GET | `/redfish/v1/Chassis/1` | シャーシリソース |
| GET | `/novnc/` | noVNC UI へリダイレクト |
| GET | `/novnc/vnc.html` | ブラウザ VNC コンソール |
| GET | `/websockify` | WebSocket-to-VNC プロキシ |

## IPMI コマンド

| コマンド | 説明 |
|---------|------|
| Get Device ID | BMC 識別情報 |
| Get Channel Auth Capabilities | 認証方式ネゴシエーション |
| Get Chassis Status | 電源状態取得 |
| Chassis Control | 電源オン/オフ/サイクル/リセット |
| Set/Get Boot Options | ブートデバイス変更 |

## 環境変数

### BMC 設定

| 変数 | デフォルト | 説明 |
|------|-----------|------|
| `QMP_SOCK` | `/var/run/qemu/qmp.sock` | QMP ソケットパス |
| `IPMI_USER` | `admin` | 認証ユーザー名 |
| `IPMI_PASS` | `password` | 認証パスワード |
| `REDFISH_ADDR` | `127.0.0.1` | Redfish バインドアドレス |
| `REDFISH_PORT` | `8080` | Redfish HTTP ポート |
| `IPMI_PORT` | `6623` | IPMI UDP ポート |
| `SERIAL_ADDR` | `localhost:9002` | SOL ブリッジ先 |
| `TLS_CERT` | (空) | TLS 証明書パス（`TLS_CERT` と `TLS_KEY` の両方を設定した場合のみ TLS を有効化） |
| `TLS_KEY` | (空) | TLS 鍵パス |
| `VM_BOOT_MODE` | `bios` | デフォルトブートモード (`bios` または `uefi`) |
| `VM_IPMI_ADDR` | (空、無効) | VM IPMI chardev リッスンアドレス (例: `:9002`) |
| `VNC_ADDR` | `localhost:5900` | noVNC プロキシが接続する QEMU VNC アドレス |
| `POWER_ON_AT_START` | `false` | 起動時に VM を自動的に電源オンにする（MAAS を使わない構成で有用） |

### コンテナ設定

`docker/entrypoint.sh` が QEMU 引数を構成するために使用:

| 変数 | デフォルト | 説明 |
|------|-----------|------|
| `VM_MEMORY` | `2048` | VM メモリ (MB) |
| `VM_CPUS` | `2` | VM CPU 数 |
| `ENABLE_KVM` | `true` | KVM アクセラレーション使用 (TCG にフォールバック) |
| `VNC_PORT` | `5900` | VNC ディスプレイポート |
| `VM_DISK` | `/vm/disk.qcow2` | VM ディスクイメージパス |
| `VM_CDROM` | (空) | CD-ROM ISO パス |
| `VM_BOOT` | `c` | ブートデバイス (c=ディスク, d=CD-ROM, n=ネットワーク) |
| `VM_BOOT_MENU_TIMEOUT` | `0` | ブートメニュータイムアウト (ms、0=無効) |
| `VM_NETWORKS` | (空) | TAP パススルー用ホストインターフェース (カンマ区切り、インターフェース出現まで最大30秒待機、containerlab 対応) |
| `QEMU_EXTRA_ARGS` | (空) | 追加 QEMU 引数 |
| `DEBUG` | `false` | デバッグ出力を有効化 |

## 開発

```bash
# ビルド
go build ./cmd/qemu-bmc
make docker-build

# ユニットテスト
go test ./... -count=1
go test ./... -race -count=1

# 静的解析
go vet ./...

# コンテナ統合テスト
make container-test         # クイックスモークテスト
make container-test-all     # 全 82 テスト (9 カテゴリ)
```

## アーキテクチャ

```
cmd/qemu-bmc/main.go          # エントリポイント
internal/
  qmp/                         # QMP ソケットクライアント
  qemu/                        # QEMU プロセス管理
  machine/                     # VM 状態管理
  redfish/                     # Redfish HTTP サーバー (gorilla/mux)
  ipmi/                        # IPMI UDP サーバー + VM chardev サーバー (RMCP/RMCP+)
  novnc/                       # noVNC 静的ファイル（埋め込み）+ WebSocket-to-VNC プロキシ
  bmc/                         # BMC 設定状態 (ユーザー、LAN、チャネル)
  config/                      # 環境変数設定
docker/
  Dockerfile                   # マルチステージビルド (Go ビルダー + Debian ランタイム)
  entrypoint.sh                # 環境変数 → QEMU 引数変換
  setup-network.sh             # TAP/ブリッジネットワーク設定
tests/                         # Bash ベースのコンテナ統合テスト
containerlab/                  # containerlab トポロジ例
```

## ライセンス

TBD
