# Một Dockerfile dùng chung cho cả ba service; tham số SERVICE chọn package
# trong cmd/. Ngữ cảnh build là thư mục gốc repo để cây internal/ dùng chung
# nhìn thấy được.
#
#   docker build -f build/Dockerfile --build-arg SERVICE=kyc -t kalapa-kyc:dev .

# --- tầng 1: dependency ----------------------------------------------------
# Chép go.mod/go.sum trước nghĩa là lớp cache module chỉ bị huỷ khi danh sách
# dependency đổi, chứ không phải mỗi lần sửa mã nguồn.
FROM golang:1.26-alpine AS deps
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# --- tầng 2: biên dịch -----------------------------------------------------
FROM deps AS build
ARG SERVICE
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH
COPY . .
# CGO_ENABLED=0 tạo ra file nhị phân TĨNH — chính điều đó cho phép tầng cuối là
# distroless gần như `scratch`, không có libc. -trimpath cùng -ldflags=-s -w
# bỏ đường dẫn build và bảng ký hiệu: image nhỏ hơn khoảng 30% và build tái lập được.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/app ./cmd/${SERVICE}

# --- tầng 3: môi trường chạy -----------------------------------------------
# distroless/static-debian12 không có shell, không có trình quản lý gói và không
# có file nhị phân setuid. Đó là một biện pháp kiểm soát thật chứ không phải mỹ
# phẩm: nó xoá bỏ trọn vẹn nhóm kỹ thuật "chui vào pod rồi đi tiếp" sau khi đã
# khai thác được.
FROM gcr.io/distroless/static-debian12:nonroot
# Chứng chỉ CA đi kèm sẵn trong base image — đó là thứ cần để kết nối TLS tới
# Keycloak hoặc một API bên ngoài. UID 65532 là user `nonroot` của distroless;
# securityContext trong chart ghim đúng con số đó để hai bên không thể lệch nhau.
COPY --from=build /out/app /app
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/app"]
