# Plan: CLI Go bật "remote host server" + app React Native điều khiển từ xa

## Context (Tại sao làm cái này)

Bạn muốn một bộ công cụ cá nhân kiểu **TeamViewer/Termius bỏ túi tự xây**:

- Chạy **1 lệnh CLI** trên máy cần điều khiển (host) → nó bật một server.
- Mở **app React Native** trên điện thoại → quét QR để ghép đôi → kết nối vào host.
- Từ app, truy cập **từ bất cứ đâu qua Internet**: gõ lệnh terminal, duyệt/tải file, xem CPU/RAM/tiến trình, và gọi các API tùy chỉnh của riêng bạn.

Quyết định đã chốt với người dùng:
- **Chức năng**: Terminal/shell + Quản lý file + Giám sát hệ thống + Custom API.
- **Mạng**: Từ xa qua Internet (cần xuyên NAT, không bắt user cấu hình router).
- **Stack server/CLI**: Go (build ra 1 binary, chạy mọi nơi).
- **Bảo mật**: QR pairing + token.

Đây là dự án mới, chưa có code. Plan này mô tả kiến trúc + lộ trình build từ đầu.

---

## Kiến trúc tổng thể

Vấn đề lõi: máy host thường nằm sau NAT/firewall, không có IP public → app không "gọi vào" trực tiếp được. Giải pháp: **cả host và app đều mở kết nối ĐI RA (outbound)** tới một **Relay** trung gian có IP public. Relay chỉ làm nhiệm vụ nối ống (pipe bytes) giữa hai bên theo `deviceID`.

```
┌─────────────┐      outbound wss      ┌──────────────┐      outbound wss     ┌──────────────┐
│  Host (Go)  │ ─────────────────────► │   Relay      │ ◄──────────────────── │  App (RN)    │
│  CLI server │   register(deviceID)   │  (VPS public)│   connect(deviceID)   │  client      │
└─────────────┘                        └──────────────┘                       └──────────────┘
   PTY / FS / metrics / custom              ghép ống theo deviceID                 UI điều khiển
```

Có **2 thành phần code** (lúc đầu):

1. **`hostd` — CLI + server (Go)**: chạy trên máy host. Bật các "service" (terminal, file, monitor, custom), lắng nghe WebSocket, in QR pairing ra terminal.
2. **`mobile` — app React Native (Expo)**: quét QR, lưu token, UI cho 4 nhóm chức năng.

### Tầng mạng = chỉ là 1 URL (endpoint-agnostic)

**Nguyên tắc cốt lõi: cách host "lộ ra Internet" chỉ là 1 chuỗi URL trong QR/config, KHÔNG dính vào core.** App chỉ biết "dial tới URL này"; `hostd` chỉ biết "lắng nghe ở đây". Protocol WebSocket/envelope y hệt nhau dù URL là gì.

Vì vậy không cần quyết hạ tầng ngay. Lộ trình về mạng:

- **Bây giờ (dev/local)**: chạy thẳng `ws://127.0.0.1:PORT` (hoặc IP LAN). Đủ để build & test toàn bộ chức năng. **Không cần tunnel, không cần relay, không cần VPS.**
- **Khi cần remote**: chỉ việc **đổi hostname trong QR** — không sửa code core:
  - **Tailscale** (khuyên cho cá nhân): cài 2 đầu → IP cố định `100.x` + P2P trực tiếp, mượt nhất, mã hóa sẵn.
  - **Cloudflare Tunnel**: URL public, không cần cài client phía điện thoại — nhưng public nên **bắt buộc** token verify mọi channel + nên đặt Cloudflare Access ở edge.
  - **Relay tự viết / Headscale**: nếu sau này muốn tự chủ hoàn toàn.

Tóm lại: build core trước trên `127.0.0.1`, hạ tầng mạng nhét vào sau bằng cách đổi URL.

---

## Giao thức (Protocol)

Một kết nối **WebSocket (wss)** duy nhất giữa app ↔ host, multiplex nhiều "channel" qua một envelope JSON; riêng dữ liệu nhị phân (terminal output, file bytes) gửi qua binary frame.

```
Envelope:  { "ch": "term"|"fs"|"sys"|"api", "type": "...", "id": "<reqId>", "data": {...} }
```

- **`term`** (terminal): `open` (mở PTY, kèm cols/rows), `input` (bytes gõ vào), `output` (bytes ra — binary frame), `resize`, `close`.
- **`fs`** (file): `list` (đường dẫn → danh sách), `read`/`download`, `write`/`upload`, `delete`, `mkdir`, `stat`. Request/response theo `reqId`.
- **`sys`** (monitor): `subscribe` → server đẩy metric định kỳ (CPU, RAM, disk, network, top processes); `kill <pid>`.
- **`api`** (custom): `call` với tên handler + payload → chạy handler do bạn đăng ký → trả kết quả. Đây là điểm mở rộng để bạn nhét logic riêng.

Thiết kế envelope theo `ch` giúp thêm chức năng mới mà không phá vỡ client cũ.

---

## Tech stack & thư viện đề xuất

### Host CLI (`hostd`, Go)
- CLI framework: **`spf13/cobra`** (lệnh `hostd start`, `hostd pair`, `hostd status`).
- WebSocket: **`coder/websocket`** (tên cũ nhooyr) — modern, context-aware.
- PTY (terminal thật): **`creack/pty`** — cấp shell `$SHELL` cho client.
- System metrics: **`shirou/gopsutil/v3`** — CPU, mem, disk, net, process.
- QR ra terminal: **`mdp/qrterminal/v3`** (render ASCII QR ngay trong console).
- Token/ký: **`golang-jwt/jwt/v5`** hoặc token ngẫu nhiên 256-bit + HMAC.
- Config/secret lưu tại `~/.config/hostd/` (deviceID, token đã cấp, relay URL).

### Tầng mạng — không cần lib riêng lúc đầu
- Dev: `hostd` lắng nghe `ws://127.0.0.1:PORT` (hoặc `0.0.0.0` cho LAN). Không cần thư viện gì thêm.
- Remote (sau): **Tailscale** (cài ngoài, không phải code) hoặc **Cloudflare Tunnel** (`cloudflared` chạy cạnh, không nhúng vào binary). Chỉ là đổi hostname trong QR.
- Nếu sau này tự viết `relay`: cùng `coder/websocket`, in-memory map `deviceID → hostConn`, copy bytes 2 chiều, TLS do reverse proxy (Caddy) lo.

### App (`mobile`, React Native + Expo)
- **Expo** (managed) cho nhanh; nếu cần module native thì prebuild.
- QR scan: **`expo-camera`** (có barcode scanner) hoặc `react-native-vision-camera`.
- WebSocket: API `WebSocket` có sẵn của RN.
- Terminal UI: nhúng **`xterm.js`** trong **`react-native-webview`** (cách thực dụng nhất để render terminal đầy đủ trên RN). Stream bytes ↔ WebView qua `postMessage`.
- Lưu token an toàn: **`expo-secure-store`**.
- Navigation: **`expo-router`** hoặc React Navigation; 4 tab: Terminal / Files / Monitor / Custom.

---

## Luồng bảo mật & pairing (QR + token)

1. `hostd start` lần đầu sinh `deviceID` (ổn định) + `pairingSecret` ngắn hạn (hết hạn ~2 phút).
2. CLI in QR chứa JSON: `{ url: <relay/tunnel wss URL>, deviceID, pairingCode }`.
3. App quét QR → mở wss tới url → gửi `pair` kèm `pairingCode`.
4. Host xác thực `pairingCode` → cấp **token dài hạn** (ký HMAC/JWT, gắn deviceID) → app lưu vào SecureStore.
5. Các lần sau: app kết nối + gửi token; host verify token trước khi cho mở channel nào.
6. **Khuyến nghị mạnh**: trong lúc pairing, hai bên trao đổi khóa (ECDH) để **mã hóa đầu-cuối (E2E)** payload nhạy cảm → kể cả relay/tunnel cũng không đọc được terminal/file. Có thể để v1.5 nếu muốn ra mắt sớm.
7. Lệnh `hostd revoke` để thu hồi token thiết bị bị mất.

### Đăng nhập lại khi mất kết nối / ở xa nhà (KHÔNG cần QR lại)

Nguyên tắc: **QR chỉ dùng đúng 1 lần** lúc pair đầu tiên. Sau đó việc kết nối lại hoàn toàn dựa vào token đã lưu — vì khi ở xa bạn không thể lấy lại QR từ terminal của host.

Để token cũ luôn dùng lại được, phải đảm bảo các thứ sau **persistent trên host** (lưu ở `~/.config/hostd/`, không sinh mới mỗi lần `start`):
- **`deviceID` cố định** → app luôn tìm đúng host qua relay mà không cần quét lại.
- **Khóa ký token cố định** (HMAC secret / JWT signing key) → token đã cấp vẫn verify đúng sau khi host **reboot / chạy lại CLI**. Đây là cái bẫy chính: nếu khóa sinh ngẫu nhiên mỗi lần chạy thì mọi token thành vô hiệu và buộc phải QR lại.
- **Danh sách token/thiết bị đã cấp** (để hỗ trợ revoke và refresh).

Mô hình token: dùng cặp **access token (ngắn hạn) + refresh token (dài hạn, có thể vô thời hạn tới khi revoke)**:
- App lưu cả hai trong SecureStore.
- Mất mạng → app **tự reconnect** (backoff) dùng access token; nếu access token hết hạn → dùng refresh token để lấy access token mới (silent re-login, người dùng không thấy gì).
- Chỉ khi **refresh token bị revoke hoặc xóa app** mới cần pair lại bằng QR (lúc đó bạn buộc phải về gần máy host — đúng kỳ vọng bảo mật).

Phía mạng (chỉ là chuyện URL cố định hay không):
- **Local `127.0.0.1` / LAN**: URL cố định trong môi trường đó → reconnect ổn.
- **Tailscale**: MagicDNS hostname **cố định** → app reconnect từ bất cứ đâu, kể cả host vừa reboot. Lựa chọn tốt nhất cho remote.
- **Cloudflare named tunnel**: domain **cố định** → reconnect ổn. (Tránh `trycloudflare` free vì URL đổi mỗi lần chạy.)
Điểm chung: chỉ cần endpoint URL **không đổi** thì reconnect-bằng-token chạy tốt — và đó là thứ ta cấu hình, không phải code.

Hệ quả thiết kế: `hostd start` phải **idempotent** về danh tính — đọc lại deviceID/khóa/token store cũ nếu đã có, chỉ tạo mới khi chưa tồn tại.

---

## Session profiles & sandbox (bảo mật theo từng phiên)

Mục tiêu: **mỗi session có thể chạy dưới một "profile" khác nhau** để cô lập. Admin → vào thẳng host; khách → bị nhốt trong sandbox (không thấy file/tiến trình/mạng ngoài phạm vi cho phép).

**Thiết kế (đã chốt, triển khai ở M5):** trừu tượng hoá việc spawn shell sau một interface — core không quan tâm sandbox kiểu gì:

```go
type Launcher interface {
    StartShell(p Profile) (*Session, error)   // channel term gọi cái này thay vì spawn $SHELL trực tiếp
}
```

- **Profile** mô tả quyền của session: thư mục/rootfs được thấy, có cô lập mạng không (+ policy), giới hạn CPU/RAM/PID, và được phép dùng channel/custom-API nào. **Token của thiết bị → ánh xạ tới một profile.**
- Các implementation của `Launcher` (chọn theo profile):
  - `direct` — PTY thẳng trên host. **Dành cho admin.** Đây là cái M1–M3 dùng (làm trước).
  - `bwrap`/`namespaces` (Linux) — bó namespace `mount`+`pid`+`net`(netns)+`user`+`uts`+`ipc` + cgroups. Dùng **bubblewrap** cho gọn, không tự code namespace.
  - `sandbox-exec` (macOS) — Seatbelt profile.
  - `container` (Podman/Docker) — cô lập mạnh nhất, nặng hơn.

**Lưu ý quan trọng:**
- `netns` **một mình không đủ** — nó chỉ cô lập mạng; shell vẫn đọc được mọi file/tiến trình. Muốn an toàn phải dùng cả bó namespace ở trên.
- Namespaces là **Linux-only**. macOS phải dùng `sandbox-exec` hoặc VM/container Linux. Cơ chế cụ thể quyết khi tới M5 (chưa chốt OS host).
- Vì core gọi qua interface `Launcher`, M1–M3 cứ chạy `direct`; thêm sandbox sau **không phải sửa channel `term`**.

---

## Cấu trúc thư mục dự kiến

```
remote-host/
├── go.mod
├── cmd/
│   └── hostd/main.go        # CLI entrypoint (cobra)
│   # (relay/ chỉ thêm về sau nếu tự host hạ tầng — không cần ở giai đoạn đầu)
├── internal/
│   ├── server/              # websocket hub, envelope router theo ch
│   ├── term/                # PTY qua creack/pty
│   ├── fs/                  # file ops
│   ├── sys/                 # gopsutil metrics
│   ├── customapi/           # registry handler tùy chỉnh
│   ├── launcher/            # Launcher interface: direct (M1) → bwrap/sandbox-exec/container (M5)
│   ├── auth/                # pairing, token, profile-per-session, (E2E key sau)
│   └── qr/                  # render QR + đóng gói payload pairing
└── mobile/                  # Expo React Native app
    ├── app/                 # expo-router screens: pair, term, files, monitor, custom
    ├── lib/ws.ts            # client websocket + envelope
    └── lib/protocol.ts      # type envelope dùng chung (mirror Go)
```

---

## Lộ trình triển khai (milestones)

**M1 — Khung xương + Terminal qua LAN (chứng minh hoạt động)**
- Dựng `cmd/hostd` với cobra, `internal/server` mở wss local (`ws://:PORT`).
- Channel `term`: mở PTY, stream in/out. Test bằng `wscat` hoặc web nhỏ.
- App Expo tối thiểu: nhập URL tay → xterm.js trong WebView gõ được lệnh.

**M2 — Pairing QR + token**
- `internal/auth` + `internal/qr`: in QR, pairing handshake, cấp/verify token.
- App: màn quét QR (expo-camera) → lưu token (SecureStore) → auto-connect.

**M3 — File + Monitor + Custom API**
- Channel `fs`: list/read/write/upload/download/delete.
- Channel `sys`: subscribe metrics (gopsutil), kill process.
- Channel `api`: registry để bạn đăng ký handler riêng + ví dụ mẫu.
- App: 3 tab UI tương ứng.

**M4 — Remote qua Internet (chỉ đổi hostname, KHÔNG sửa core)**
- Vì endpoint đã tách thành URL config, chỉ cần trỏ QR sang hostname remote:
  - **Tailscale** (mặc định khuyên dùng): cài 2 đầu, QR chứa `ws://<magicdns>:PORT`. Test từ 4G — đục NAT P2P, đo độ trễ.
  - hoặc **Cloudflare Tunnel**: `cloudflared` trên host → `wss://<domain>`, nhúng vào QR. Vì public → kiểm tra kỹ token verify + cân nhắc Cloudflare Access.
- Reconnect/backoff, heartbeat khi mạng chập chờn.

**M5 — Hardening + sandbox theo profile + (tùy chọn) hạ tầng tự chủ**
- **Session profiles + sandbox**: triển khai `Launcher` ngoài `direct` — `bwrap` (Linux) hoặc `sandbox-exec` (macOS) tuỳ OS host chốt lúc đó. Token → profile; admin = `direct`, khách = sandbox.
- `hostd revoke`, rate-limit pairing, audit log.
- (Tùy chọn) E2E encryption, multi-device.
- (Tùy chọn) Tự host relay/Headscale nếu không muốn phụ thuộc bên thứ ba.

---

## Verification (cách kiểm thử end-to-end)

- **M1**: chạy `go run ./cmd/hostd start`, dùng `wscat -c ws://localhost:PORT` (hoặc app) gửi `term/open` rồi `term/input` `"ls\n"` → nhận `output` đúng nội dung thư mục.
- **M2**: chạy `hostd start` → QR hiện trong terminal → app quét → kiểm tra token được lưu (SecureStore) và lần mở app thứ 2 tự kết nối không cần quét lại. Thử token sai → bị từ chối.
- **M3**: từ app, `fs/list` thư mục home khớp với `ls`; upload 1 file rồi kiểm tra tồn tại trên host; tab Monitor hiển thị CPU/RAM cập nhật; gọi 1 custom handler trả đúng kết quả.
- **M4/M5**: tắt WiFi điện thoại, dùng 4G (khác mạng host) → vẫn pair & điều khiển được. Đo độ trễ gõ phím terminal. Kill tiến trình host rồi bật lại → app tự reconnect (M5).
- **Re-login không QR (quan trọng)**: sau khi pair xong, **tắt hẳn `hostd` rồi `start` lại** → app phải tự kết nối lại bằng token cũ, KHÔNG hiện màn quét QR. Kiểm tra `deviceID` và khóa ký trong `~/.config/hostd/` không đổi giữa 2 lần chạy. Để access token hết hạn (hoặc chỉnh TTL ngắn để test) → app tự dùng refresh token lấy token mới mà không bắt người dùng làm gì. Chạy `hostd revoke` → lần kết nối sau bị từ chối và app mới yêu cầu pair lại.
- Toàn tuyến: kiểm tra traffic là `wss://` (TLS); với E2E (nếu làm) xác nhận relay chỉ thấy ciphertext.

---

## Rủi ro & lưu ý

- **An ninh là tối quan trọng**: server này cho phép chạy shell từ xa → nếu lộ là chiếm máy. Bắt buộc token verify trước mọi channel, pairing code hết hạn nhanh, nên có E2E. Cảnh báo rõ trong README.
- **xterm.js trong WebView**: cầu nối postMessage có thể là điểm nghẽn hiệu năng/độ trễ — cần test kỹ ở M1.
- **Tầng mạng để sau**: vì endpoint chỉ là URL, đừng để quyết định Tailscale/Cloudflare chặn việc build core. Làm xong chức năng trên `127.0.0.1` rồi mới gắn hostname remote. Lưu ý `trycloudflare` free đổi URL mỗi lần chạy → nếu dùng Cloudflare thì chọn named tunnel.
- **gopsutil & PTY** hành xử khác nhau giữa macOS/Linux/Windows → ưu tiên macOS/Linux trước, Windows sau.
