# Tài liệu

> **Chưa biết gì về hạ tầng?** Bộ này là tài liệu tham khảo và giả định bạn đã
> biết Pod, Service, Deployment là gì. Hãy bắt đầu từ
> [giáo trình 15 bài](course/README.md) rồi quay lại đây.

Đọc theo thứ tự; mỗi phần giả định bạn đã đọc phần trước.

| | File | Nội dung |
|---|---|---|
| 0 | [00-architecture.md](00-architecture.md) | Cái gì nói chuyện với cái gì, namespace, luồng cấu hình và telemetry, và những thứ cố tình không có |
| 1 | [01-yas-mapping.md](01-yas-mapping.md) | Từng pattern hạ tầng của YAS: ở đây nằm đâu, đã đổi gì, và vì sao |
| 2 | [02-kubernetes-deep-dive.md](02-kubernetes-deep-dive.md) | Probe, QoS, tắt mềm, rolling update, HPA, PDB, NetworkPolicy, Pod Security, operator |
| 3 | [03-observability.md](03-observability.md) | Ba tín hiệu, cardinality của label, truyền trace qua Kafka, và các liên kết nối chúng lại |
| 4 | [04-gitops-with-argocd.md](04-gitops-with-argocd.md) | App-of-apps, sync wave, AppProject, drift, `ignoreDifferences`, secret |
| 5 | [05-ci-cd.md](05-ci-cd.md) | Vì sao CI tạo commit thay vì deploy; Dockerfile; kiểm tra manifest và policy |
| 6 | [06-runbook.md](06-runbook.md) | Vận hành ngày thứ hai, scale, nâng cấp, chạy với ít RAM hơn |
| 7 | [07-labs.md](07-labs.md) | Mười hai bài tập cố tình làm hỏng hệ thống |
| 8 | [08-troubleshooting.md](08-troubleshooting.md) | Triệu chứng → nguyên nhân → cách sửa |
