# 01 — YAS → Kalapa, đối chiếu từng pattern

Mọi thứ thuộc về hạ tầng mà YAS làm, ở đây nó nằm đâu, và cái gì đã thay đổi.
Nên đọc tài liệu này với repo YAS mở bên cạnh; mục đích là để bạn di chuyển
được qua lại giữa hai bên.

## Cấu trúc Helm chart

| YAS | Kalapa |
|---|---|
| `k8s/charts/backend/` — chart gốc | `deploy/charts/go-service/` |
| `k8s/charts/product/` — chart mỏng, phụ thuộc `file://../backend` | `deploy/charts/kyc/`, cùng pattern |
| `k8s/charts/yas-configuration/` — ConfigMap + Secret dùng chung | `deploy/charts/kalapa-config/` |
| `values.template.yaml` + `create-charts.sh` | không tái hiện — ba service thì chưa cần bộ sinh |

**Giống hệt:** một chart gốc, các chart con chỉ ghi đè values, và một
dependency `file://` để chart gốc đi cùng một commit.

**Thêm vào chart gốc:** `startupProbe`, `PodDisruptionBudget`,
`NetworkPolicy`, `topologySpreadConstraints`, annotation `checksum/config` trên
pod, biến môi trường lấy từ Downward API, và một `securityContext` tường minh
thoả mãn Pod Security Standard mức `restricted`. YAS để trống
`podSecurityContext` và `resources` — nghĩa là pod của nó chạy bằng root và
nằm trong QoS class BestEffort.

**Đã đổi:** `extraApplicationConfigPaths` của YAS ánh xạ sang
`SPRING_CONFIG_ADDITIONAL_LOCATION` của Spring. Ở đây cùng ý tưởng đó là
`CONFIG_EXTRA_FILES`, do `internal/platform/config/config.go` đọc. Cùng cách
phân tầng, nhưng là ~80 dòng Go thay vì một framework.

## Cấu hình và tự restart khi đổi

| YAS | Kalapa |
|---|---|
| `yas-configuration-configmap` chứa `application.yaml` | ConfigMap `kalapa-config` chứa `config.yaml` |
| ConfigMap riêng từng service (`product-application-configmap`, …) | `kalapa-<svc>-config`, sinh bằng vòng `range` trên `serviceConfig` |
| `yas-postgresql-credentials-secret` và các secret tương tự | `kalapa-postgres-credentials`, `kalapa-keycloak-credentials` |
| Reloader là dependency của chart `yas-configuration` | Reloader là một Argo CD Application riêng |

**Vì sao Reloader được tách ra:** dưới GitOps, một controller là thành phần
nền tảng có vòng đời riêng. Gói nó thành subchart của phần cấu hình nghĩa là
mỗi lần đổi cấu hình thì manifest của controller cũng bị render lại, và phiên
bản controller bị ghim bởi người sửa chart cấu hình gần nhất.

**Cũng thêm vào:** annotation `checksum/config` trên pod template. Reloader lo
trường hợp ConfigMap bị sửa tại chỗ; checksum lo trường hợp `helm upgrade`.
YAS chỉ dựa vào Reloader.

## Database

| YAS | Kalapa |
|---|---|
| postgres-operator của Zalando | CloudNativePG |
| `postgresql.postgres:5432` | `kalapa-db-rw.data:5432` |
| mỗi service một database (`product`, `cart`, …) | một database, tách theo bảng |
| `patroni.slots` cho logical replication | đã bật `wal_level: logical`, chưa tạo slot |

**Vì sao CloudNativePG:** nó là operator được phát triển tích cực hơn, các
Service của nó (`-rw`, `-ro`, `-r`) làm cho việc tách primary/replica hiện ra
ngay trong connection string, và nó không cần cấu hình Patroni riêng. Operator
của Zalando hoàn toàn ổn và khái niệm chuyển qua trực tiếp — một CR
`postgresql` và một CR `Cluster` mô tả cùng một thứ.

**Vì sao chỉ một database:** thêm một CloudNativePG Cluster nữa tốn ~250 MB.
Trong hệ thống thật thì nên mỗi service một database, vì database dùng chung
là khoá migration dùng chung và bán kính thiệt hại dùng chung.

## Kafka

| YAS | Kalapa |
|---|---|
| Strimzi có khối `zookeeper:` | Strimzi 1.x, KRaft, `KafkaNodePool` |
| 3 broker + 3 ZooKeeper | 1 node gộp controller+broker |
| Debezium Connect cho CDC | chưa deploy (lab 11) |
| AKHQ làm giao diện web | `make kafka-console` |

**Cái gì đã đổi và vì sao:** ZooKeeper đã bị gỡ hoàn toàn khỏi Kafka. Strimzi
1.x từ chối `spec.zookeeper` và bắt buộc phải có ít nhất một `KafkaNodePool`.
Nếu bạn đang đọc `kafka-cluster.yaml` của YAS thì khối `zookeeper:` và các
trường `replicas`/`storage` nằm dưới `spec.kafka` đều đã lỗi thời — replicas
và storage giờ nằm trong node pool.

**Thêm vào ở đây:** truyền trace context qua chặng Kafka
(`internal/platform/events/carrier.go`). YAS được OpenTelemetry Java agent lo
hộ miễn phí. Trong Go thì không có gì tự instrument Kafka cho bạn, nên
producer ghi `traceparent` vào header của record và consumer đọc nó ra. Thiếu
nó, Tempo sẽ hiển thị hai trace không liên quan và service graph mất cạnh
`kyc → scoring`. Đây là mảnh hay bị bỏ sót nhất trong một hệ tracing
microservice.

## Định danh

| YAS | Kalapa |
|---|---|
| Keycloak Operator + CR `Keycloak` | Deployment + Service + Ingress viết tay |
| CR `KeycloakRealmImport` | realm JSON trong ConfigMap, `--import-realm` |
| Spring Security resource server | `internal/gateway/auth.go`, thư viện chuẩn |
| BFF riêng cho backoffice và storefront | một gateway duy nhất |

**Vì sao không dùng operator:** thêm một CRD và một controller nữa (~200 MB)
chỉ để quản một replica. Deployment viết tay làm mọi nút điều chỉnh hiện rõ —
`KC_HOSTNAME`, cổng management mà health probe phải trỏ tới, cache `local` giúp
giảm nửa heap.

**Điều duy nhất cần mang theo:** `KC_HOSTNAME` quyết định claim `iss` trong mọi
token. Nếu nó không khớp với giá trị mà gateway dùng để verify thì mọi token
đều bị từ chối, kèm một thông báo lỗi chẳng nhắc gì đến hostname. Đây là thứ
hỏng đầu tiên khi bạn bật xác thực, ở cả hai stack.

## Observability

| YAS | Kalapa | Ghi chú |
|---|---|---|
| OpenTelemetry Operator + CR `OpenTelemetryCollector` | Helm chart `opentelemetry-collector` | operator thêm khả năng tiêm auto-instrumentation, mà Go đằng nào cũng không dùng được |
| Promtail → Collector → Loki | Alloy → Loki | **Promtail đã ngừng hỗ trợ**; Alloy là bản thay thế |
| `kube-prometheus-stack` | như vậy, phiên bản 92 | |
| Tempo với `metricsGenerator` | như vậy | cộng thêm `overrides` tường minh để generator thực sự chạy |
| grafana-operator + CR `GrafanaDatasource` | ConfigMap do sidecar nạp | bớt một operator, cùng kết quả |
| OTel Java agent (tự động) | OTel Go SDK (thủ công) | Go không có agent bytecode; instrument phải viết rõ |
| logback pattern `traceId=%X{traceId:-}` | `log/slog` + handler đọc từ context | không gặp vấn đề MDC/thread-local |

**Chuyện Promtail → Alloy là thay đổi đáng kể nhất.** Mô hình tư duy y hệt:
phát hiện pod, đọc `/var/log/pods`, gắn label, đẩy vào Loki. Ngôn ngữ cấu hình
là River thay vì YAML. Bảng đối chiếu:

| Promtail | Alloy |
|---|---|
| `scrape_configs.kubernetes_sd_configs` | `discovery.kubernetes` |
| `relabel_configs` | `discovery.relabel` |
| `pipeline_stages: [cri: {}]` | `loki.process { stage.cri {} }` |
| `pipeline_stages: [json: {...}]` | `stage.json { expressions = {...} }` |
| `clients: [{url: ...}]` | `loki.write { endpoint { url = ... } }` |

Hãy đọc `deploy/argocd/platform/14-alloy-logs.yaml` cạnh
`promtail.values.yaml` của YAS — vẫn là năm bước giống nhau.

**Cũng thêm vào:** kỷ luật về cardinality của label. Cấu hình collector của
YAS đẩy `traceId` thành một Loki label:

```yaml
- action: insert
  key: loki.attribute.labels
  value: namespace,container,pod,level,traceId   # <- traceId
```

Cách đó tạo ra một Loki stream cho mỗi trace. Trên lab thì chạy được; ở lưu
lượng thật nó phá nát index. Ở đây `trace_id` nằm trong thân log và được tìm
bằng biểu thức lọc, còn `derivedFields` của Grafana datasource biến nó thành
một link bấm được. Cùng trải nghiệm, nhưng cardinality có giới hạn. Phần này
được giải thích kỹ ở [03-observability.md](03-observability.md#quy-tắc-cardinality).

## Triển khai

| YAS | Kalapa |
|---|---|
| `deploy-yas-applications.sh` — vòng lặp `helm upgrade` kèm `sleep 60` | Argo CD app-of-apps với sync wave |
| `setup-cluster.sh` — khoảng 15 lệnh `helm install` | `deploy/argocd/platform/*.yaml` |
| `cluster-config.yaml` + `yq` để vá file values | values viết thẳng trong Application manifest |
| mệnh lệnh, chạy từ laptop | khai báo, được kéo về từ Git |

Đây là khác biệt cấu trúc lớn nhất. Script của YAS là một cách hoàn toàn hợp
lý để dựng cluster, và chúng có ba vấn đề chỉ lộ ra theo thời gian:

1. **`sleep 60` không phải là kiểm tra sẵn sàng.** Nó quá dài khi mọi thứ
   nhanh và quá ngắn khi mọi thứ chậm. Sync wave thì chờ tới trạng thái
   *Healthy*.
2. **Trạng thái cluster là bất cứ thứ gì người chạy script gần nhất tạo ra.**
   Drift vô hình. Argo CD hiển thị nó là `OutOfSync` và, với `selfHeal`, hoàn
   nguyên nó.
3. **Rollback nghĩa là phải nhớ values trước đó là gì.** Với GitOps thì chỉ là
   `git revert`.

Riêng pattern dùng `yq` vá file values đáng nhắc tên:

```bash
# YAS: setup-cluster.sh sửa một file đang được theo dõi trước khi cài
grafana_hostname="grafana.$DOMAIN" yq -i '.hostname=env(grafana_hostname)' \
  ./observability/prometheus.values.yaml
```

Cách này để lại working tree bẩn và khiến file đã commit không phải file thực
sự được apply. Ở đây values nằm thẳng trong Application manifest, còn mẩu
trạng thái thật sự khác nhau theo từng lần cài (URL của repo) thì được thay
một lần bởi `scripts/init-repo.sh` rồi commit — nhờ vậy repo vẫn tự mô tả
chính nó trọn vẹn.

## CI

| YAS | Kalapa |
|---|---|
| mỗi service một workflow (16 file gần như giống hệt) | một workflow matrix với bộ lọc đường dẫn |
| `workflow-template.yaml` copy bằng tay | `dorny/paths-filter` tự tính matrix |
| image gắn tag `:latest`, đẩy lên GHCR | image gắn tag theo short SHA |
| deploy: chạy lại shell script | CI commit tag mới; Argo CD đồng bộ |
| `chart-releaser` publish chart lên gh-pages | không cần — Argo CD đọc chart ngay trong repo |
| gitleaks chạy hằng đêm | gitleaks chạy trên mọi PR |
| SonarCloud, OWASP Dependency-Check, JaCoCo | `go vet`, `golangci-lint`, `govulncheck`, `-race`, Trivy |

**Vấn đề `:latest` đáng dừng lại suy nghĩ.** YAS đẩy `:latest` và chart của nó
cũng kéo `:latest`. Dưới GitOps điều đó phá vỡ đảm bảo cốt lõi: Argo CD so
sánh manifest trong Git với manifest trong cluster, cả hai đều ghi `:latest`,
nó báo `Synced`, và nó không hề biết rằng tag đó giờ trỏ tới một digest khác.
Cluster có thể đang chạy code cũ tuỳ ý trong khi dashboard vẫn xanh. Việc CI
ghi một tag bất biến vào `values.yaml` mới là thứ làm cho chữ "Synced" có
nghĩa.

## Những thứ YAS có mà ở đây không tái hiện

- **16 service.** Ba là đủ để minh hoạ cả đường đồng bộ lẫn bất đồng bộ.
- **Elasticsearch + Kibana.** ~2 GB cho một tính năng tìm kiếm không liên quan
  gì tới các pattern hạ tầng.
- **Debezium CDC.** Một bài lab hay (lab 11), không phải thành phần nền.
- **Giao diện Next.js.** Không phải hạ tầng.
- **Tích hợp PayPal, gợi ý sản phẩm, Azure OpenAI.** Tính năng ứng dụng.
