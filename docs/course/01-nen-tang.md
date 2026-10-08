# 01 — Server, process, port

> **Bài này trả lời:** Chạy một ứng dụng trên máy chủ thật ra là gì? Vì sao
> việc đó lại khó đến mức sinh ra cả một ngành?
>
> **Cần biết trước:** biết mở terminal, biết `cd`, `ls`.

---

## 1. Lý thuyết

### 1.1 Một ứng dụng đang chạy là một *process*

Khi bạn chạy `./app`, hệ điều hành tạo ra một **process**. Process có:

- một **PID** (số định danh)
- vùng **bộ nhớ** riêng, process khác không đọc được
- một hoặc nhiều **thread** để thực thi
- một tập **file đang mở** (gồm cả socket mạng)
- một **user** mà nó chạy dưới quyền (`root`, `tiendat`, …)

Thử ngay:

```bash
# Chạy một web server đơn giản bằng Python
python3 -m http.server 8000 &
# Xem process đó
ps aux | grep http.server
```

Cột đầu là user, cột thứ hai là PID. Ghi nhớ hai thứ này — về sau chúng xuất
hiện lại dưới dạng `runAsUser` trong Kubernetes.

### 1.2 Port là cách process nhận kết nối

Một máy chỉ có một địa chỉ IP nhưng chạy nhiều ứng dụng. **Port** là con số từ
1 đến 65535 để phân biệt chúng.

```bash
# Xem cái gì đang nghe ở port nào (macOS)
lsof -iTCP -sTCP:LISTEN -P -n | head
```

Ba quy tắc cần thuộc:

1. **Một port chỉ một process nghe được.** Chạy hai app cùng port 8080 thì cái
   thứ hai chết với `address already in use`.
2. **Port < 1024 cần quyền root.** Đó là lý do web server thật thường chạy ở
   8080 rồi để thứ khác đứng trước ở port 80.
3. **Port là của máy, không phải của ứng dụng.** Hai ứng dụng muốn cùng port
   80 thì phải ở hai máy khác nhau — hoặc hai *không gian mạng* khác nhau, và
   đó chính là điều container làm được.

### 1.3 Vì sao đưa ứng dụng lên server lại khó

Giả sử app của bạn chạy ngon trên máy bạn. Giờ đưa lên server. Danh sách những
thứ có thể khác nhau:

| Thứ | Máy bạn | Server |
|---|---|---|
| Phiên bản ngôn ngữ | Go 1.26 | Go 1.19 |
| Thư viện hệ thống | glibc 2.39 | glibc 2.31 |
| Biến môi trường | `DATABASE_URL=localhost` | chưa đặt |
| File cấu hình | có `config.yaml` cạnh binary | không có |
| User chạy | `tiendat` (có quyền ghi `/tmp`) | `appuser` (không có) |
| Múi giờ | `Asia/Ho_Chi_Minh` | `UTC` |
| Port 8080 | rảnh | app khác đang chiếm |

Mỗi dòng là một lần "chạy trên máy tôi mà". Cộng thêm: app của bạn không phải
app duy nhất trên server đó, và app hàng xóm có thể cần glibc phiên bản khác.

### 1.4 Ba cách người ta đã thử giải quyết

**Cách 1 — Tài liệu cài đặt.** Một file `INSTALL.md` dài, ai cũng làm sai một
bước. Không tái lập được.

**Cách 2 — Máy ảo (VM).** Đóng gói cả hệ điều hành. Tái lập được, nhưng mỗi VM
tốn vài GB RAM và khởi động mất vài chục giây tới vài phút. Chạy 20
microservice là 20 hệ điều hành.

**Cách 3 — Container.** Đóng gói ứng dụng cùng thư viện của nó, **dùng chung
nhân (kernel) của máy chủ**. Nhẹ như process, tái lập như VM. Đây là thứ bài
02 nói tới.

---

## 2. Trong repo này nằm đâu

Dù đã lên Kubernetes, ba khái niệm trên vẫn hiện nguyên hình.

### Process chạy dưới user nào

`build/Dockerfile:39`

```dockerfile
USER 65532:65532
```

Số 65532 là một **UID** — đúng cái cột user bạn thấy trong `ps aux`. Ứng dụng
không chạy bằng root.

Và `deploy/charts/go-service/values.yaml:89`

```yaml
podSecurityContext:
  runAsNonRoot: true
  runAsUser: 65532                 # phải khớp USER trong Dockerfile
```

> **Vì sao hai chỗ phải khớp:** Dockerfile quyết định file trong image thuộc
> về UID nào; `runAsUser` quyết định process chạy dưới UID nào. Lệch nhau thì
> process không đọc được chính file của mình → `permission denied`.

### Port

`deploy/charts/go-service/values.yaml:24`

```yaml
ports:
  http: 8080   # traffic nghiệp vụ
  admin: 9090  # /healthz/*, /metrics
```

Hai port, hai mục đích. Code mở chúng ở `internal/platform/httpx/server.go`,
trong hàm `Run` — tìm `appSrv` và `adminSrv`.

> **Vì sao tách hai port:** nếu `/metrics` và `/healthz` nằm chung port với
> traffic người dùng, thì khi app bị quá tải, hàng đợi request sẽ làm health
> check cũng chậm theo → Kubernetes tưởng app chết và giết nó, trong khi nó
> chỉ đang bận. Bài 07 nói kỹ.

### Biến môi trường

`internal/platform/config/config.go:143`, hàm `applyEnv` — đây là nơi mọi biến
môi trường được đọc vào cấu hình.

---

## 3. Thực hành

### 3.1 Tự tay va vào vấn đề

```bash
# Terminal 1
python3 -m http.server 8080
# Terminal 2 — thử chiếm cùng port
python3 -m http.server 8080
```

Bạn sẽ thấy `OSError: [Errno 48] Address already in use`. Đây là lý do mỗi
container cần không gian mạng riêng.

### 3.2 Xem một process thật của repo này

Nếu bạn đã có cluster chạy (chưa có cũng không sao, quay lại sau bài 03):

```bash
# Xem process bên trong một container
kubectl -n kalapa get pods
kubectl -n kalapa exec deploy/kyc -- /app --help 2>&1 | head
```

Lệnh trên sẽ **thất bại**. Vì sao? Vì image là distroless — không có shell,
không có `ps`. Đó là chủ ý, bài 14 giải thích.

### 3.3 Quan sát port từ bên ngoài

```bash
kubectl -n kalapa port-forward deploy/kyc 9090:9090 &
curl -s localhost:9090/healthz/live
curl -s localhost:9090/metrics | head -5
kill %1
```

`port-forward` dựng một đường hầm từ máy bạn vào port 9090 của pod. Đây là
công cụ debug bạn sẽ dùng hàng ngày.

---

## 4. Tự kiểm

Trả lời được hết thì sang bài 02.

- [ ] Process có những gì? (kể được ít nhất 4 thứ)
- [ ] Vì sao hai ứng dụng không thể cùng nghe port 8080 trên một máy?
- [ ] Vì sao web server thật hay chạy ở port 8080 thay vì 80?
- [ ] Kể ba thứ có thể khác nhau giữa máy bạn và server, khiến app chạy được ở
      đây mà không chạy được ở kia.
- [ ] Container khác VM ở điểm căn bản nào?
- [ ] Trong repo này, UID của process ứng dụng là bao nhiêu, và nó được khai
      báo ở mấy chỗ?

---

## 5. Bẫy thường gặp

| Bẫy | Thực tế |
|---|---|
| "App chạy được là xong" | Chạy được trên máy bạn không nói gì về server. Khác biệt nằm ở môi trường, không ở code. |
| "Cứ chạy bằng root cho nhanh" | Một lỗ hổng trong app chạy root là một lỗ hổng toàn máy chủ. |
| "Port nào cũng như nhau" | Port < 1024 cần root. Việc cần root chỉ để nghe port 80 đã khiến vô số hệ thống chạy toàn bộ app bằng root. |

---

**Bài tiếp:** [02 — Container và Docker](02-container-va-docker.md)
