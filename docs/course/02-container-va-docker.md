# 02 — Container và Docker

> **Bài này trả lời:** Container thực sự là gì (không phải "máy ảo nhẹ")?
> Image, layer, registry là gì? Và Dockerfile của repo này làm gì từng dòng?
>
> **Cần xong bài:** [01](01-nen-tang.md)

---

## 1. Lý thuyết

### 1.1 Container không phải máy ảo

Câu so sánh "container là VM nhẹ" sai và sẽ làm bạn hiểu lầm về sau. Sự thật:

> **Container chỉ là một process bình thường của Linux, bị giới hạn tầm nhìn
> và tài nguyên.**

Không có hệ điều hành thứ hai. Không có nhân thứ hai. Chỉ có một process, chạy
trên nhân của máy chủ, nhưng bị bịt mắt bằng ba cơ chế có sẵn của Linux:

| Cơ chế | Làm gì | Hệ quả bạn thấy |
|---|---|---|
| **namespace** | Giới hạn process *nhìn thấy* gì | Trong container, `ps` chỉ thấy process của chính nó; nó có mạng riêng nên port 8080 của nó không đụng port 8080 của container khác |
| **cgroup** | Giới hạn process *dùng được* bao nhiêu | "Tối đa 96 MB RAM và 0,3 CPU" — vượt RAM thì bị giết |
| **filesystem ảnh** | Cho process một cây thư mục riêng | Trong container, `/` là nội dung của image, không phải `/` của máy chủ |

Đây là lý do container khởi động trong mili-giây còn VM mất vài chục giây: VM
phải boot cả một nhân, container chỉ là `fork()` + `exec()` kèm vài cờ.

Hệ quả quan trọng: **container dùng chung nhân với máy chủ.** Container Linux
không chạy được trên nhân Windows. (Docker Desktop trên macOS thật ra chạy một
VM Linux ẩn bên dưới — nên trên máy bạn, container đang nằm *trong* một VM.)

### 1.2 Image, layer, container

Ba từ hay bị dùng lẫn lộn:

| Từ | Là gì | Ví von |
|---|---|---|
| **Image** | Một gói chỉ-đọc gồm filesystem + metadata | File `.app` hoặc `.exe` |
| **Container** | Một image *đang chạy*, cộng thêm một lớp ghi được | Tiến trình đang chạy |
| **Layer** | Một tầng thay đổi filesystem trong image | Một commit trong git |

Image gồm nhiều **layer** xếp chồng. Mỗi lệnh trong Dockerfile tạo một layer.
Layer được **chia sẻ và cache**: nếu hai image cùng dùng `golang:1.26-alpine`
thì layer đó chỉ lưu một lần trên đĩa và chỉ tải một lần.

Đây là lý do thứ tự dòng trong Dockerfile quan trọng kinh khủng:

```dockerfile
# TỆ: đổi một dòng code -> phải tải lại toàn bộ dependency
COPY . .
RUN go mod download
RUN go build

# TỐT: đổi code -> chỉ build lại, dependency vẫn trong cache
COPY go.mod go.sum ./
RUN go mod download     # layer này chỉ đổi khi go.mod đổi
COPY . .
RUN go build
```

Khác biệt thực tế: build 30 giây so với build 3 phút, mỗi lần.

### 1.3 Registry

**Registry** là kho chứa image. Docker Hub, GitHub Container Registry (ghcr.io),
Amazon ECR…

Tên image đầy đủ có bốn phần:

```
ghcr.io / kalapa-lab / kalapa-kyc : a3f2c91b4e07
└─────┘   └────────┘   └────────┘   └──────────┘
registry    owner        tên         tag
```

**Tag là nhãn dán, không phải danh tính.** Tag `latest` hôm nay trỏ tới image
A, mai có thể trỏ tới image B. Danh tính thật của image là **digest** — một mã
băm SHA-256 của nội dung:

```
ghcr.io/kalapa-lab/kalapa-kyc@sha256:e3b0c44298fc1c149afb...
```

Ghi nhớ điều này. Nó là gốc rễ của một vấn đề lớn ở bài 12.

### 1.4 Multi-stage build

Để build một app Go bạn cần trình biên dịch Go (~400 MB). Để *chạy* nó bạn chỉ
cần file nhị phân (~25 MB). Multi-stage build cho phép dùng một image để build
và một image khác, nhỏ xíu, để chạy — chỉ chép file nhị phân sang.

---

## 2. Trong repo này nằm đâu

Toàn bộ nằm trong một file: `build/Dockerfile`. Đọc từng stage.

### Stage 1 — tải dependency (dòng 9–12)

```dockerfile
FROM golang:1.26-alpine AS deps
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
```

- `FROM ... AS deps` — đặt tên cho stage để stage sau tham chiếu được.
- Chỉ chép `go.mod` và `go.sum`, **chưa** chép code. Nhờ vậy layer này chỉ bị
  hỏng cache khi danh sách dependency đổi.
- `--mount=type=cache` — một cache của BuildKit tồn tại qua các lần build,
  không nằm trong image cuối.

### Stage 2 — biên dịch (dòng 15–30)

```dockerfile
FROM deps AS build
ARG SERVICE
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/app ./cmd/${SERVICE}
```

| Thứ | Nghĩa |
|---|---|
| `ARG SERVICE` | Tham số lúc build. Một Dockerfile dùng cho cả ba service: `--build-arg SERVICE=kyc` |
| `CGO_ENABLED=0` | Không liên kết thư viện C → file nhị phân **tĩnh**, chạy được trên image không có libc |
| `-trimpath` | Bỏ đường dẫn tuyệt đối khỏi binary → build tái lập được, và stack trace không lộ `/Users/tiendat/...` |
| `-ldflags="-s -w"` | Bỏ bảng ký hiệu và thông tin debug → nhỏ hơn ~30% |

> `CGO_ENABLED=0` là dòng quan trọng nhất ở đây. Thiếu nó, binary sẽ cần libc
> lúc chạy, và stage 3 không có libc → container khởi động là chết ngay với
> `no such file or directory` (một thông báo lỗi cực kỳ đánh lạc hướng: file
> *có* ở đó, thứ thiếu là thư viện mà nó cần).

### Stage 3 — runtime (dòng 34–41)

```dockerfile
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/app"]
```

`distroless/static` chứa đúng ba thứ: chứng chỉ CA gốc, thông tin múi giờ, và
vài file user tối thiểu. **Không có shell, không có trình quản lý gói, không
có `ls`, không có `cat`.**

`COPY --from=build` — chỉ lấy file nhị phân từ stage trước. Toàn bộ trình biên
dịch Go bị bỏ lại.

---

## 3. Thực hành

### 3.1 Build thử và đo

```bash
cd ~/Documents/resource/kalapa-platform
docker build -f build/Dockerfile --build-arg SERVICE=kyc -t kalapa-kyc:thu .
docker images kalapa-kyc:thu
```

Kết quả phải quanh **27 MB**.

### 3.2 Chứng minh cache layer thực sự hoạt động

```bash
# Lần 1 (đã có ở trên) — chậm
# Giờ sửa một dòng code rồi build lại
echo '// thêm một dòng ghi chú' >> internal/kyc/kyc.go
time docker build -f build/Dockerfile --build-arg SERVICE=kyc -t kalapa-kyc:thu .
```

Để ý các dòng `CACHED` trong output — stage tải dependency không chạy lại. Bây
giờ thử phá cache:

```bash
# Khôi phục file
git checkout internal/kyc/kyc.go
# Chạm vào go.mod -> cache dependency vỡ
touch go.mod
time docker build -f build/Dockerfile --build-arg SERVICE=kyc -t kalapa-kyc:thu .
```

Lần này chậm hơn hẳn. Đó chính là lý do thứ tự dòng trong Dockerfile quan trọng.

### 3.3 Nhìn vào các layer

```bash
docker history kalapa-kyc:thu
```

Bạn sẽ thấy image cuối chỉ có vài layer, và layer lớn nhất chính là binary.

### 3.4 Chứng minh distroless không có shell

```bash
docker run --rm -it kalapa-kyc:thu sh
# -> lỗi: không tìm thấy executable
docker run --rm -it --entrypoint ls kalapa-kyc:thu /
# -> cũng lỗi
```

Đây **không phải** bất tiện vô cớ. Bài 14 sẽ cho thấy nó loại bỏ cả một họ kỹ
thuật tấn công.

### 3.5 Tự tay gây ra lỗi `CGO_ENABLED`

```bash
# Tạm sửa Dockerfile: đổi CGO_ENABLED=0 thành CGO_ENABLED=1
sed -i.bak 's/CGO_ENABLED=0/CGO_ENABLED=1/' build/Dockerfile
docker build -f build/Dockerfile --build-arg SERVICE=kyc -t kalapa-kyc:hong .
docker run --rm kalapa-kyc:hong
# Quan sát thông báo lỗi. Rồi khôi phục:
mv build/Dockerfile.bak build/Dockerfile
```

Hãy nhớ mặt thông báo lỗi đó. Nó sẽ quay lại ám bạn vào một đêm nào đó.

---

## 4. Tự kiểm

- [ ] Container khác VM ở chỗ nào? (nói đúng về nhân)
- [ ] Ba cơ chế Linux tạo nên container là gì, mỗi cái làm gì?
- [ ] Vì sao `COPY go.mod` phải đứng trước `COPY . .`?
- [ ] Image, container, layer khác nhau thế nào?
- [ ] Tag khác digest ở điểm nào? Vì sao điều đó quan trọng?
- [ ] Multi-stage build tiết kiệm được gì trong repo này? (nêu con số)
- [ ] `CGO_ENABLED=0` mở ra khả năng gì ở stage cuối?
- [ ] Vì sao `USER 65532` phải khớp `runAsUser` trong Helm chart?

---

## 5. Bẫy thường gặp

| Bẫy | Thực tế |
|---|---|
| `COPY . .` ngay đầu Dockerfile | Mọi lần sửa code đều tải lại toàn bộ dependency |
| Dùng `ubuntu:latest` làm base cho app Go | 70 MB thừa, kèm hàng trăm gói hệ thống phải vá CVE suốt đời |
| Cài `curl`, `vim` vào image "cho dễ debug" | Chính là công cụ kẻ tấn công cần. Debug bằng `kubectl debug` với ephemeral container |
| Dùng tag `latest` ở production | Không biết đang chạy cái gì, và không rollback được |
| Chạy container bằng root | Thoát container ra máy chủ dễ hơn nhiều |

---

**Bài tiếp:** [03 — Vì sao cần Kubernetes](03-vi-sao-can-kubernetes.md)
