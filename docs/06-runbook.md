# 06 — Sổ tay vận hành

Vận hành ngày thứ hai. Các câu lệnh giả định `--context kalapa`; các target
trong Makefile tự thêm giúp bạn.

---

## Vòng đời

```bash
make cluster       # minikube + ingress + metrics-server
make hosts         # /etc/hosts -> IP của node (sudo; chạy lại sau mỗi lần dựng lại)
make init          # tạo repo GitHub, thay URL vào manifest  (một lần)
make bootstrap     # cài Argo CD, apply root Application
make watch         # theo dõi quá trình hội tụ
make smoke         # test đầu-cuối
make down          # xoá sạch
```

Offline hoặc không có GitHub: `make up-local` cài đúng stack đó bằng Helm
thuần. Nó đọc chính các Argo CD Application manifest rồi phát lại, nên hai
đường không thể lệch nhau.

### Một lần khởi động nguội diễn ra thế nào

| Thời gian | Kỳ vọng |
|---|---|
| 0–3 phút | minikube lên, ingress sẵn sàng |
| 3–4 phút | Argo CD chạy, root Application được tạo |
| 4–6 phút | wave −20 đến −10: namespace, CNPG, Strimzi, Reloader |
| 6–11 phút | wave 0–2: Prometheus, Loki, Tempo, Collector, Alloy, config Grafana |
| 11–16 phút | wave 10: Postgres, Kafka, Keycloak — Kafka là cái chậm |
| 16–18 phút | wave 20–30: config, rồi ba service |

Gần như toàn bộ thời gian đó là kéo image. Lần khởi động thứ hai nhanh hơn
nhiều.

---

## Kiểm tra hằng ngày

```bash
make status                                    # bản tóm tắt một màn hình
kubectl -n argocd get applications             # cluster có đúng như Git nói không?
kubectl top pods -A --sort-by=memory | head -15
kubectl get events -A --field-selector type=Warning --sort-by=.lastTimestamp | tail
```

---

## Triển khai một thay đổi

### Thay đổi code của một service

```bash
git commit -am "kyc: siết validate số CCCD" && git push
# CI: test -> build -> đẩy lên GHCR -> commit tag mới vào values.yaml
# Argo CD: thấy commit đó, đồng bộ
make watch
```

### Thay đổi cấu hình

```bash
# sửa deploy/charts/kalapa-config/values.yaml
git commit -am "tăng pool DB lên 8" && git push
```

Argo CD cập nhật ConfigMap; Reloader restart những pod đang mount nó. Theo dõi
cả hai nửa:

```bash
kubectl -n kalapa get cm kalapa-config -o yaml | yq '.data."config.yaml"' | head
kubectl -n kalapa logs deploy/kalapa-reloader --tail=20
kubectl -n kalapa rollout status deploy/kyc
```

### Rollback

```bash
git revert <sha> && git push          # cách GitOps: có vết, apply lại được
```

Trong tình huống khẩn cấp, làm ngoài luồng:

```bash
kubectl -n kalapa rollout undo deploy/kyc
```

`selfHeal` của Argo CD sẽ hoàn nguyên việc đó trong ~2 phút. Nó mua cho bạn
thời gian, không phải một cách sửa — hãy tiếp nối bằng commit revert.

---

## Scale

```bash
# Qua Git (đúng cách)
#   deploy/charts/kyc/values.yaml -> go-service.replicaCount: 2

# Ngoài luồng (bị selfHeal hoàn nguyên trong ~2 phút)
kubectl -n kalapa scale deploy/kyc --replicas=2
```

`scoring` là ngoại lệ: scale nó vượt quá số partition của topic thì vô ích, vì
một consumer group không thể có nhiều thành viên hữu dụng hơn số partition.
Tăng cả hai:

```bash
kubectl -n kafka patch kafkatopic kyc.application.submitted \
  --type merge -p '{"spec":{"partitions":3}}'
```

---

## Soi tầng dữ liệu

```bash
make psql
#   \dt
#   SELECT status, count(*) FROM kyc_applications GROUP BY status;
#   SELECT decision, count(*) FROM credit_scores GROUP BY decision;

kubectl -n data get cluster kalapa-db -o yaml | yq '.status.instancesStatus'

make kafka-console                      # đọc topic từ đầu

kubectl -n kafka run lag-$RANDOM --rm -it --restart=Never \
  --image=quay.io/strimzi/kafka:latest-kafka-4.3.1 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server kalapa-kafka-bootstrap:9092 \
  --describe --group scoring
```

Lệnh cuối cùng cho bạn con số nói lên `scoring` có theo kịp hay không.

---

## Các giao diện

```bash
make argocd-password
make pf-grafana        # localhost:3000
make pf-prometheus     # localhost:9090
make pf-argocd         # localhost:8080
```

Hoặc, sau `make hosts`: `grafana.kalapa.local`, `argocd.kalapa.local`,
`identity.kalapa.local`, `api.kalapa.local`.

---

## Chứng chỉ

Khoá realm của Keycloak tự xoay và gateway tự nhận ra — cache JWKS của nó làm
mới mỗi 10 phút, và làm mới ngay khi gặp một key id lạ.

Nếu bạn thêm TLS (cert-manager + một ClusterIssuer tự ký), nhớ rằng gateway khi
đó cần CA trong kho tin cậy của nó. Image distroless có sẵn bundle CA công
cộng chứ không có CA của bạn; hãy mount nó vào và đặt `SSL_CERT_FILE`.

---

## Chạy với ít tài nguyên hơn

Dưới ~7 GB, hãy tắt bớt theo thứ tự này — mất ít kiến thức nhất trước:

| Tắt cái gì | Tiết kiệm | Mất gì |
|---|---|---|
| Keycloak + Postgres của nó (`22-keycloak.yaml`) | ~1,0 GB | chỉ mất lab 5; xác thực vốn mặc định tắt |
| Kafka (`21-kafka.yaml`, và `scoring`) | ~1,3 GB | toàn bộ đường bất đồng bộ — mất nhiều |
| `nodeExporter`, `kubeStateMetrics` | ~0,1 GB | metric của node và của object; metric ứng dụng vẫn chạy |
| Tempo (`12-tempo.yaml`) | ~0,3 GB | tracing — tín hiệu thú vị nhất |
| Argo CD, dùng `make up-local` | ~0,8 GB | vòng lặp GitOps; mọi thứ khác giữ nguyên |

Bỏ Keycloak và chạy `make up-local` nhét vừa toàn bộ stack trong khoảng 4,5 GB
mà vẫn giữ nguyên đường Kafka, đó là lựa chọn tốt nhất nếu bạn eo hẹp.

Thu nhỏ thay vì gỡ bỏ:

```yaml
# Prometheus
retention: 6h
# Kafka
nodePool.jvm.heap: 384m
nodePool.resources.limits.memory: 768Mi
# Keycloak
jvm.heap: 384m
```

Dưới mấy con số đó thì các JVM không còn khởi động ổn định, và bạn sẽ mất thời
gian debug cái lab thay vì học từ nó.

---

## Nâng cấp

**Phiên bản chart** được ghim trong `deploy/argocd/platform/*.yaml`. Kiểm tra
bản mới hơn:

```bash
helm repo update
helm search repo prometheus-community/kube-prometheus-stack --versions | head -3
```

Nâng từng cái một, commit, rồi xem đúng Application đó đồng bộ. Nâng nhiều thứ
cùng lúc làm cho lỗi khó quy trách nhiệm.

**Bản thân Kubernetes**: `K8S_VERSION=v1.35.0 make cluster` trên một profile
mới. Đừng nâng cấp tại chỗ trên lab — dựng lại tốn 15 phút và nó kiểm chứng
rằng repo thực sự mô tả trọn vẹn cluster.

**CRD của operator** không phải lúc nào cũng nâng theo Helm chart. Cả CNPG và
Strimzi đều đóng gói CRD trong `crds/`, mà Helm chỉ cài chứ không bao giờ cập
nhật. Hãy đọc release note của operator trước một lần nâng major.

---

## Sao lưu và khôi phục

Lab không có, một cách có chủ ý — vì không có object store. Nếu có thì nó sẽ
trông thế này:

```yaml
# CloudNativePG, trong spec của Cluster
backup:
  barmanObjectStore:
    destinationPath: s3://kalapa-backups/
    s3Credentials: { ... }
  retentionPolicy: "30d"
```

và khôi phục là một `Cluster` mới với `bootstrap.recovery` trỏ tới đường dẫn đó.

Thứ bạn làm được ngay hôm nay:

```bash
kubectl -n data exec kalapa-db-1 -- pg_dump -U kalapa kalapa > backup.sql
```

Câu trả lời thật thà là: thứ duy nhất đáng sao lưu ở đây là Git repo, và
GitHub đang giữ nó. Mọi thứ còn lại tái tạo được bằng `make down && make up` —
và đó chính là tính chất mà GitOps sinh ra để có.
