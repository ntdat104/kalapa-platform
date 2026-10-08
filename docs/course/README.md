# Giáo trình Infra & DevOps từ con số 0

Bộ tài liệu `docs/00` đến `docs/08` là **tài liệu tham khảo**: nó giả định bạn
đã biết Pod là gì, Service là gì. Bộ này thì ngược lại — nó là **giáo trình**,
viết cho người chưa từng động vào hạ tầng, và dẫn bạn đến đúng điểm có thể đọc
được bộ kia.

## Cách học bộ này

Mỗi bài có đúng bốn phần, luôn theo thứ tự đó:

| Phần | Nó làm gì |
|---|---|
| **Lý thuyết** | Khái niệm, và quan trọng hơn: *vấn đề nào sinh ra nó*. Một công nghệ chỉ có nghĩa khi bạn biết nó thay thế cho nỗi đau gì. |
| **Trong repo này nằm đâu** | Đúng file, đúng dòng, kèm trích đoạn. Đây là phần biến lý thuyết thành thứ sờ được. |
| **Thực hành** | Lệnh để gõ thật trên cluster đang chạy. Có cả lệnh phá hỏng rồi sửa lại. |
| **Tự kiểm** | Câu hỏi bạn phải trả lời được trước khi sang bài sau. Không trả lời được thì đọc lại, đừng đi tiếp. |

**Quy tắc quan trọng nhất:** đừng đọc suông. Mở hai cửa sổ — một cái tài liệu,
một cái terminal — và gõ từng lệnh. Hạ tầng là thứ chỉ vào đầu qua ngón tay.

## Lộ trình

### Phần A — Nền tảng (chưa biết gì → hiểu container)

| Bài | Tên | Trả lời câu hỏi |
|---|---|---|
| [01](01-nen-tang.md) | Server, process, port | Chạy một ứng dụng trên máy chủ thật ra là gì? Vì sao nó khó? |
| [02](02-container-va-docker.md) | Container và Docker | Container giải quyết nỗi đau nào? Image, layer, registry là gì? |
| [03](03-vi-sao-can-kubernetes.md) | Vì sao cần Kubernetes | Có Docker rồi thì thiếu gì nữa? Khai báo vs mệnh lệnh. |

### Phần B — Kubernetes cốt lõi

| Bài | Tên | Trả lời câu hỏi |
|---|---|---|
| [04](04-pod-va-deployment.md) | Pod, ReplicaSet, Deployment | Đơn vị chạy nhỏ nhất là gì? Label/selector hoạt động ra sao? |
| [05](05-service-dns-ingress.md) | Service, DNS, Ingress | Pod có IP thay đổi liên tục, vậy gọi nhau kiểu gì? Ra Internet kiểu gì? |
| [06](06-configmap-va-secret.md) | ConfigMap và Secret | Cấu hình để đâu? Mật khẩu để đâu? Đổi rồi thì sao? |
| [07](07-tai-nguyen-probe-vong-doi.md) | Tài nguyên, probe, vòng đời | Vì sao pod bị giết? Vì sao deploy rớt request? |

### Phần C — Đóng gói và trạng thái

| Bài | Tên | Trả lời câu hỏi |
|---|---|---|
| [08](08-helm.md) | Helm | 10 service × 8 file YAML = 80 file. Làm sao sống nổi? |
| [09](09-database-va-operator.md) | Database và Operator | Chạy Postgres trong k8s kiểu gì? CRD và Operator là gì? |
| [10](10-kafka-va-bat-dong-bo.md) | Kafka và bất đồng bộ | Khi nào không nên gọi HTTP? Event-driven là gì? |

### Phần D — Vận hành

| Bài | Tên | Trả lời câu hỏi |
|---|---|---|
| [11](11-observability.md) | Observability | Hệ thống chậm. Làm sao biết chậm ở đâu? |
| [12](12-gitops-argocd.md) | GitOps với Argo CD | Ai được quyền chạm vào production? Rollback kiểu gì? |
| [13](13-cicd.md) | CI/CD | Từ `git push` đến pod mới chạy, có những gì ở giữa? |
| [14](14-bao-mat.md) | Bảo mật | Những thứ tối thiểu phải làm, và vì sao. |
| [15](15-hoc-tiep-gi.md) | Học tiếp gì | Bản đồ phần còn lại của thế giới infra. |

## Cần chuẩn bị gì

```bash
# macOS
brew install docker minikube kubectl helm go jq yq

# Kiểm tra
docker version && minikube version && kubectl version --client && helm version && go version
```

Docker Desktop cần được cấp **ít nhất 10 GB RAM** (Settings → Resources →
Memory). Lý do nằm trong [README gốc](../../README.md#ngân-sách-bộ-nhớ).

Chưa cần dựng cluster ngay — bài 03 sẽ bảo bạn lúc nào thì dựng.

## Ước lượng thời gian

| Phần | Thời gian đọc + làm |
|---|---|
| A (bài 01–03) | 3–4 giờ |
| B (bài 04–07) | 8–10 giờ |
| C (bài 08–10) | 6–8 giờ |
| D (bài 11–15) | 10–12 giờ |

Tổng khoảng **30–35 giờ** nếu làm thật. Đừng cố nhồi trong một tuần; kiến thức
hạ tầng cần thời gian lắng, và phần lớn việc học thật diễn ra lúc bạn phá hỏng
thứ gì đó rồi phải sửa.

## Sau khi học xong

Quay lại đọc [docs/00 → docs/08](../README.md). Lúc đó chúng sẽ đọc như tài
liệu tham khảo chứ không còn như tiếng nước ngoài. Rồi làm
[12 bài lab](../07-labs.md).
