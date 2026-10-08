# 00 — Kiến trúc

## Ứng dụng, tóm trong một đoạn

Khách hàng nộp một hồ sơ xác minh danh tính. **gateway** là service duy nhất
lộ ra ngoài; nó verify token của người gọi rồi chuyển tiếp request. **kyc**
kiểm tra hồ sơ, ghi xuống Postgres, rồi phát một sự kiện. **scoring** nhận sự
kiện đó, tính điểm tín dụng, và ghi xuống Postgres. Endpoint tổng hợp của
gateway đọc song song cả hai nửa.

Nghiệp vụ chỉ có thế. Ba service HTTP, một topic Kafka, một database.

## Đường đi của request

```
POST /api/kyc/applications
  │
  ├─ ingress-nginx            kết thúc TLS (nếu có), định tuyến theo host,
  │                           giới hạn kích thước body, timeout
  │
  ├─ gateway        :8080     verify JWT dựa trên JWKS của Keycloak
  │                           cắt bỏ /api, chuyển tiếp qua DNS nội bộ cluster
  │
  ├─ kyc            :8080     validate -> INSERT -> COMMIT
  │                                    -> produce kyc.application.submitted
  │                           201 Created
  │
  └─ (bất đồng bộ) ─────────► Kafka topic, 1 partition
                                │
                                └─ scoring   consume -> tính -> UPSERT
```

Có hai tính chất của đường đi này đáng nói rõ, vì toàn bộ phần nền tảng còn
lại được xây xoay quanh chúng.

**Ghi database được commit trước khi phát sự kiện.** Làm ngược lại sẽ khiến
`scoring` nhận sự kiện cho một bản ghi không tồn tại. Lỗ hổng còn lại — bản ghi
đã commit nhưng sự kiện bị mất — là có thật, và cách sửa đàng hoàng là
transactional outbox. Lab 6 sẽ cho bạn tự xây nó.

**Điểm số là nhất quán cuối cùng (eventually consistent).** Client đọc ngay
sau khi nộp sẽ nhận `scoring_pending: true`, chứ không phải một con 404 giả vờ
là lỗi. Phơi bày khoảng trễ ra tốt hơn là giấu nó sau một vòng retry mà client
không nhìn thấy.

## Vì sao là ba service này chứ không ít hơn

gateway hoàn toàn có thể thay bằng một annotation của Ingress. Nó là một
process thật vì:

- nó cho trace một chặng đầu tiên nhìn thấy được, nên bạn quan sát được việc
  truyền trace context đang hoạt động
- endpoint tổng hợp của nó gọi song song hai upstream, tạo ra đúng hình dạng
  span mà bạn cần học cách nhận ra trong Tempo
- phần verify JWT bằng Go (`internal/gateway/auth.go`) viết bằng thư viện
  chuẩn, nên ba thứ thực sự hay hỏng — sai issuer, xoay khoá, lệch đồng hồ —
  hiện ra rõ ràng thay vì bị giấu trong một thư viện

`kyc` và `scoring` tách ra vì một service đồng bộ và một consumer hướng sự
kiện có hình dạng vận hành khác nhau thật sự: readiness check khác nhau, giới
hạn scale khác nhau (số partition chứ không phải CPU), kiểu hỏng khác nhau
(consumer lag chứ không phải latency).

## Namespace

| Namespace | Chứa gì | Pod Security |
|---|---|---|
| `kalapa` | gateway, kyc, scoring, config của chúng, Reloader | `restricted` |
| `data` | các cluster CloudNativePG | `baseline` |
| `kafka` | Strimzi operator, node Kafka, entity operator | `baseline` |
| `identity` | Keycloak và database của nó | `baseline` |
| `observability` | Prometheus, Grafana, Loki, Tempo, Collector, Alloy | `privileged` |
| `argocd` | Argo CD | (mặc định) |
| `ingress-nginx` | ingress controller | (do addon quản) |

Việc tách namespace không phải để cho đẹp. Nó cho ba thứ:

1. **Một ranh giới bán kính thiệt hại.** `kubectl delete ns kalapa` xoá ứng
   dụng mà vẫn giữ nguyên dữ liệu.
2. **Một chỗ bám cho NetworkPolicy.** Policy chọn đối tượng theo label của
   namespace; mỗi tầng một namespace khiến luật đọc được.
3. **Một mức Pod Security cho mỗi tầng.** Namespace ứng dụng chạy dưới
   `restricted` — profile nghiêm nhất có sẵn. Namespace observability thì
   không thể, vì node-exporter phải mount filesystem của host. Chính việc bị
   buộc phải ghi ngoại lệ đó ra giấy mới là phần hữu ích.

## Luồng cấu hình

```
deploy/charts/kalapa-config/values.yaml
        │
        ├──► ConfigMap kalapa-config          → /etc/kalapa/config.yaml
        ├──► ConfigMap kalapa-<svc>-config    → /etc/kalapa/service/<svc>.yaml
        └──► Secret  kalapa-postgres-credentials → biến môi trường
                │
                ▼
        internal/platform/config.Load()
          mặc định → config.yaml → <svc>.yaml → môi trường
```

Tầng sau thắng tầng trước. Thông tin đăng nhập chỉ đi qua tầng môi trường, vì
nội dung ConfigMap hiện ra trong `kubectl describe`, `helm get values` và giao
diện Argo CD, còn nội dung Secret thì không.

Reloader theo dõi cả hai và restart Deployment khi một trong hai thay đổi.
Annotation `checksum/config` trên pod template làm đúng việc đó cho
`helm upgrade` khi không có Reloader — hai cơ chế, vì một thay đổi cấu hình âm
thầm không có hiệu lực là loại bug tệ nhất.

## Luồng telemetry

```
Service Go
  ├─ traces ──OTLP/gRPC──► OTel Collector ──► Tempo
  │                              └──────────► Prometheus (metric từ span)
  ├─ metrics ◄──scrape (ServiceMonitor)────── Prometheus
  └─ logs ───stdout──► /var/log/pods ──► Alloy ──► Loki
                                                     │
                                          Grafana ───┴── cả ba, liên kết chéo
```

Trace được **đẩy đi**, metric được **kéo về**. Sự bất đối xứng đó không phải
ngẫu nhiên: một trace là sự kiện xảy ra một lần và phải rời khỏi process
trước khi process chết, còn một metric là giá trị hiện tại, đẩy đi vào lúc
không ai hỏi thì vô nghĩa. Hệ quả là trace cuối cùng của một pod sắp chết vẫn
tới nơi, còn metric cuối cùng thì không — và đó chính là lý do đường tắt máy
trong `internal/platform/httpx/server.go` phải flush span processor trước khi
thoát.

Collector đứng ở giữa để các service chỉ cần biết đúng một địa chỉ telemetry,
mãi mãi. Đổi Tempo sang Jaeger là sửa một file Application manifest, không
phải deploy lại toàn bộ service.

Chi tiết: [03-observability.md](03-observability.md).

## Luồng phát hành

```
lập trình viên ──push──► GitHub Actions ──► test, build, đẩy image lên GHCR
                            │
                            └──► ghi tag image (bất biến) vào
                                 deploy/charts/<svc>/values.yaml
                                 rồi commit
                                          │
                            Argo CD ──pull┘──► đồng bộ lại cluster
```

CI không bao giờ chạm vào cluster. Nó không có kubeconfig và cũng không cần.
Việc deploy chính là một commit, nghĩa là `git log` là lịch sử deploy và
`git revert` là nút rollback.

Chi tiết: [04-gitops-with-argocd.md](04-gitops-with-argocd.md) và
[05-ci-cd.md](05-ci-cd.md).

## Những thứ cố tình không có

Nói thẳng ra các khoảng trống cũng là một phần của bài học.

| Không có | Vì sao | Nếu có thì nằm đâu |
|---|---|---|
| Service mesh | mTLS và retry tốn ~1 GB; HTTP client đã tự lo timeout rồi | Linkerd, sau lab 10 |
| Transactional outbox | lỗ hổng ghi kép được cố tình để lộ ra | lab 6 |
| Quản lý secret thật | mật khẩu dev được commit để luồng đi còn theo dõi được | lab 7 (Sealed Secrets) |
| Multi-cluster | ngân sách chỉ có một node | `destinations` của Argo CD đã hỗ trợ sẵn |
| Sao lưu | lab không có object store | `backup.barmanObjectStore` của CNPG |
| Cảnh báo | Alertmanager bị tắt để tiết kiệm ~80 MB | lab 8 |
