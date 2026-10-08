# Kalapa Platform

Một hệ thống eKYC/chấm điểm tín dụng cố tình làm thật nhỏ — ba service Go,
khoảng 160 Mi RAM — nhưng đặt bên dưới một nền tảng hạ tầng cố tình làm thật
đầy đủ: Kubernetes, Helm, Argo CD, CloudNativePG, Strimzi Kafka, Keycloak và
trọn bộ observability theo chuẩn OpenTelemetry.

Chính cái tỷ lệ đó mới là mục đích. Phần ứng dụng đơn giản đến mức đọc mười
phút là nắm hết, nên toàn bộ thứ bạn thực sự đang học là hạ tầng bao quanh nó.

Hệ thống được dựng theo mẫu [YAS của NashTech](https://github.com/nashtech-garage/yas),
một nền tảng tham chiếu 16 service viết bằng Spring Boot. Mọi pattern hạ tầng
bên đó đều được tái hiện ở đây, một số đã được hiện đại hoá;
[docs/01-yas-mapping.md](docs/01-yas-mapping.md) là bảng đối chiếu chi tiết.

---

## Trong này có gì

```
                           ┌─────────────────────────────────────┐
     git push ───────────► │  GitHub Actions                     │
                           │  test ─► build image ─► ghi tag mới  │
                           └──────────────┬──────────────────────┘
                                          │ commit
                           ┌──────────────▼──────────────────────┐
                           │  repo này = trạng thái mong muốn    │
                           └──────────────┬──────────────────────┘
                                          │ pull
                           ┌──────────────▼──────────────────────┐
                           │  Argo CD   (app-of-apps, sync waves)│
                           └──────────────┬──────────────────────┘
  ┌───────────────────────────────────────▼──────────────────────────────────┐
  │ minikube                                                                 │
  │                                                                          │
  │  ingress-nginx ──► gateway ──► kyc ──────► CloudNativePG (data)           │
  │                       │          └──────► Kafka ──► scoring ──► CNPG      │
  │                       └──► Keycloak (identity)                            │
  │                                                                          │
  │  mọi pod ──OTLP──► OTel Collector ───┬─► Tempo        (traces)            │
  │                                      └─► Prometheus   (metrics)           │
  │  mọi pod ──stdout──► Alloy ────────────► Loki         (logs)              │
  │                                           └─► Grafana (cả ba, liên kết)   │
  └──────────────────────────────────────────────────────────────────────────┘
```

| Tầng | Thành phần | Vì sao chọn cái này |
|---|---|---|
| GitOps | Argo CD 3.5 | App-of-apps, sync wave, AppProject, ApplicationSet |
| Đóng gói | Helm 3 | Một chart gốc, ba chart con mỏng — đúng pattern của YAS |
| Database | CloudNativePG 1.30 | Postgres do operator quản: failover mà ứng dụng không phải đổi gì |
| Sự kiện | Strimzi 1.2 (Kafka 4.3, KRaft) | Kafka do operator quản, không còn ZooKeeper |
| Định danh | Keycloak 26 | OIDC, realm viết thành code, JWT được verify ngay tại gateway |
| Metrics | Prometheus + Grafana | ServiceMonitor CR, exemplar, dashboard RED |
| Logs | Alloy → Loki | JSON có cấu trúc, kỷ luật về cardinality của label |
| Traces | OTel Collector → Tempo | Bao gồm cả việc truyền trace context qua Kafka |
| Cấu hình | ConfigMap + Secret + Reloader | Cấu hình phân tầng, tự restart khi đổi |
| Services | Go 1.26, distroless | Image 27 MB, mỗi service tốn ~25 MB RSS |

---

## Bắt đầu nhanh

Cần: Docker Desktop cấp **≥ 10 GB** RAM, `minikube`, `kubectl`, `helm`,
`go` ≥ 1.25, và `gh` (đã đăng nhập) nếu muốn chạy vòng lặp GitOps thật.

```bash
git clone <repo này> && cd kalapa-platform

make cluster      # minikube + ingress + metrics-server      (~3 phút)
make hosts        # trỏ *.kalapa.local về node               (cần sudo)

gh auth login
make init         # tạo repo GitHub, ghi URL vào các manifest
git add -A && git commit -m "Initial platform" && git push -u origin main

make bootstrap    # cài Argo CD, apply root Application
make watch        # xem cluster hội tụ                        (~12 phút)
make smoke        # test đầu-cuối, bao gồm cả telemetry
```

Không có tài khoản GitHub, hoặc đang offline? `make up-local` cài đúng stack đó
bằng Helm thuần, bỏ qua Argo CD. Bạn chỉ mất vòng lặp GitOps, không mất gì khác.

Sau đó:

| | |
|---|---|
| Argo CD | <http://argocd.kalapa.local> — `admin` / `make argocd-password` |
| Grafana | <http://grafana.kalapa.local> — `admin` / `admin` |
| Keycloak | <http://identity.kalapa.local> — `admin` / `admin` |
| API | <http://api.kalapa.local/api/version> |

---

## Chưa biết gì về hạ tầng? Bắt đầu từ giáo trình

Bộ tài liệu bên dưới là **tài liệu tham khảo** — nó giả định bạn đã biết Pod và
Service là gì. Nếu chưa, hãy bắt đầu từ
**[giáo trình 15 bài](docs/course/README.md)**: nó dạy từ "process là gì" cho
tới GitOps, mỗi bài gồm lý thuyết → chỉ rõ file và dòng trong repo này →
thực hành trên cluster thật → tự kiểm. Khoảng 30–35 giờ nếu làm nghiêm túc.

## Tài liệu tham khảo

Đọc theo thứ tự. Mỗi tài liệu được viết để đọc song song với một cluster đang
chạy, và các câu lệnh trong đó là để bạn gõ thật.

| | | |
|---|---|---|
| 0 | [Kiến trúc](docs/00-architecture.md) | Cái gì nói chuyện với cái gì, và vì sao hình dạng lại như vậy |
| 1 | [Đối chiếu YAS → Kalapa](docs/01-yas-mapping.md) | Từng pattern của YAS nằm ở đâu tại đây, cái gì đã đổi |
| 2 | [Kubernetes chuyên sâu](docs/02-kubernetes-deep-dive.md) | Probe, QoS, tắt mềm, HPA, PDB, NetworkPolicy, PSA |
| 3 | [Observability](docs/03-observability.md) | Ba trụ cột, và các liên kết biến chúng thành một hệ thống |
| 4 | [GitOps với Argo CD](docs/04-gitops-with-argocd.md) | App-of-apps, sync wave, drift, secret, rollback |
| 5 | [CI/CD](docs/05-ci-cd.md) | Vì sao pipeline tạo một commit thay vì gọi kubectl |
| 6 | [Sổ tay vận hành](docs/06-runbook.md) | Vận hành ngày thứ hai, từng câu lệnh một |
| 7 | [Bài lab](docs/07-labs.md) | Mười hai bài tập cố tình làm hỏng hệ thống |
| 8 | [Xử lý sự cố](docs/08-troubleshooting.md) | Triệu chứng → nguyên nhân → cách sửa |

---

## Bố cục repo

```
cmd/                      ba package main
internal/
  platform/               dùng chung: config, logging, tracing, metrics, http, db, kafka
  gateway/ kyc/ scoring/  phần nghiệp vụ (nhỏ, cố ý như vậy)
build/Dockerfile          một multi-stage build cho cả ba service
deploy/
  charts/
    go-service/           chart gốc — nên đọc file này đầu tiên
    gateway/ kyc/ scoring/  các chart mỏng bọc quanh nó
    kalapa-config/        ConfigMap + Secret dùng chung
    postgres/ kafka/ keycloak/   tầng dữ liệu, dưới dạng custom resource
  argocd/
    bootstrap/            values của Argo CD + đúng một root Application
    projects/             AppProject (ranh giới phân quyền)
    platform/             Application cho hạ tầng, sắp theo sync wave
    apps/                 Application cho các service Kalapa
  manifests/              namespace, datasource và dashboard của Grafana
.github/workflows/        CI (Go) và kiểm tra manifest
scripts/                  vòng đời cluster, smoke test, sinh tải
```

---

## Ngân sách bộ nhớ

Đo thật trên một minikube một node với mọi thứ đang chạy, theo namespace:

| Namespace | Requests | Limits | Bên trong có gì |
|---|---:|---:|---|
| `observability` | 1682 Mi | 3136 Mi | Prometheus, Grafana, Loki, Tempo, Collector, Alloy, exporter |
| `kafka` | 1216 Mi | 1792 Mi | Strimzi operator, một node Kafka gộp, entity operator |
| `argocd` | 672 Mi | 1344 Mi | controller, repo-server, server, applicationset, redis |
| `identity` | 832 Mi | 1152 Mi | Keycloak và Postgres riêng của nó |
| `data` | 256 Mi | 320 Mi | Postgres của ứng dụng |
| **`kalapa`** | **160 Mi** | **320 Mi** | **ba service Go + Reloader** |
| `cnpg-system` | 96 Mi | 192 Mi | CloudNativePG operator |
| `kube-system`, `ingress-nginx` | 460 Mi | 170 Mi | add-on của control plane |
| **tổng** | **5374 Mi** | **8426 Mi** | |

Ba service Go chiếm **3% tổng requests**. Đó là tỷ lệ thật của một nền tảng
microservice nhỏ, và cũng là lý do repo này dồn công sức vào đúng chỗ nó đang dồn.

### Chọn kích thước node

Requests (5,4 GB) là phần scheduler giữ chỗ. Limits (8,4 GB) là phần mọi thứ
có thể dùng cùng lúc. Cộng thêm ~1,2 GB cho bản thân control plane:

| `--memory` của minikube | Chạy được không? |
|---|---|
| **8 GB** | thoải mái — limits vừa đủ, không tranh chấp |
| 7 GB | ổn ở trạng thái bình thường; hơi giật khi nhiều pod restart cùng lúc |
| 6,5 GB | **đo thật: limits chạm 105% allocatable.** Vẫn hội tụ và chạy được, nhưng khi vài thành phần restart đồng thời thì node bị bỏ đói và API server treo một hai phút |
| < 6 GB | phải bỏ bớt thành phần — xem [docs/06-runbook.md](docs/06-runbook.md#chạy-với-ít-tài-nguyên-hơn) |

Vậy nên: cấp cho Docker Desktop **≥ 10 GB** rồi chạy `MEMORY=8g make cluster`.
Nếu ít hơn, `make up-local` (không Argo CD) giải phóng 1,3 GB limits, và bỏ
Keycloak giải phóng thêm 1,2 GB nữa — hai cái đó cộng lại thì 5 GB là vừa.
