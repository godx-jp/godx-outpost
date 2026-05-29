# remote-host

Bộ công cụ cá nhân kiểu TeamViewer/Termius tự xây:

- **`hostd`** — CLI Go chạy trên máy host, bật một server cho phép điều khiển từ xa.
- **`relay`** — relay server (Go) trên VPS để xuyên NAT (giai đoạn v1).
- **`mobile`** — app React Native (Expo) quét QR để ghép đôi và điều khiển.

Chức năng: Terminal/shell · Quản lý file · Giám sát hệ thống (CPU/RAM/process) · Custom API.
Kết nối từ xa qua Internet (outbound tới relay/tunnel), bảo mật bằng QR pairing + token.

> ⚠️ Server này cho phép chạy shell từ xa — bảo mật là tối quan trọng. Xem phần "Rủi ro & lưu ý" trong [docs/PLAN.md](docs/PLAN.md).

## Kế hoạch

Toàn bộ kiến trúc, giao thức, tech stack và lộ trình (M1→M5) nằm trong **[docs/PLAN.md](docs/PLAN.md)**.

## Trạng thái

Dự án mới khởi tạo (scaffold). Bắt đầu từ milestone **M1 — khung `hostd` + terminal qua WebSocket LAN**.
