# 10 — Kafka và bất đồng bộ

> **Bài này trả lời:** Khi nào thì không nên gọi HTTP? Kafka thực sự là gì?
> Và vì sao "ghi database rồi phát sự kiện" là một cái bẫy?
>
> **Cần xong bài:** [09](09-database-va-operator.md)

---

## 1. Lý thuyết

### 1.1 Vấn đề với gọi đồng bộ

`kyc` nhận hồ sơ rồi cần `scoring` chấm điểm. Cách hiển nhiên là gọi HTTP:

```
client ──► kyc ──HTTP──► scoring ──► trả lời
```

Những gì hỏng theo cách đó:

| Vấn đề | Hậu quả |
|---|---|
| `scoring` chết | `kyc` cũng hỏng theo — **lỗi lan truyền** |
| `scoring` chậm 2 giây | Client chờ 2 giây, dù họ không cần điểm ngay |
| Muốn thêm `notification` | Phải sửa code `kyc` — **ràng buộc chặt** |
| `scoring` chịu tải thấp hơn | `kyc` phải tự giới hạn tốc độ |
| Request mất giữa chừng | Không ai biết, không retry được |

Gốc rễ: `kyc` **không cần** biết kết quả chấm điểm để trả lời client. Nó đang
chờ một thứ nó không cần.

### 1.2 Hướng sự kiện

Lật ngược quan hệ:

```
client ──► kyc ──► ghi DB ──► trả 201 ngay
                      │
                      └──► phát sự kiện vào Kafka
                                  │
                                  └──► scoring đọc khi nào rảnh
```

Giờ thì:

- `scoring` chết → sự kiện nằm chờ trong Kafka, xử lý sau
- `kyc` trả lời ngay, không phụ thuộc tốc độ `scoring`
- Thêm `notification` → chỉ cần một consumer mới, **`kyc` không đổi dòng nào**
- Sự kiện được lưu bền, retry được

Cái giá: **nhất quán cuối cùng**. Có một khoảng thời gian hồ sơ đã tồn tại mà
điểm chưa có. Thiết kế phải chấp nhận điều đó một cách tường minh.

### 1.3 Kafka là gì

Không phải hàng đợi tin nhắn truyền thống. Kafka là một **nhật ký chỉ-ghi-thêm
(append-only log) được phân mảnh và lưu bền**.

```
Topic "kyc.application.submitted"
└── Partition 0:  [0][1][2][3][4][5][6] ──► ghi thêm vào cuối
                            ▲        ▲
                      consumer A  consumer B
                      (offset 3)  (offset 6)
```

Khác biệt cốt lõi so với RabbitMQ/SQS:

| | Hàng đợi truyền thống | Kafka |
|---|---|---|
| Đọc xong | Tin nhắn **biến mất** | Tin nhắn **vẫn còn** tới khi hết hạn lưu |
| Vị trí đọc | Server giữ | **Consumer giữ** (offset) |
| Đọc lại | Không | Có — chỉ cần lùi offset |
| Nhiều bên đọc cùng dữ liệu | Phải nhân bản | Tự nhiên, mỗi group một offset |

Việc "đọc lại được" là siêu năng lực: thêm một service mới và cho nó xử lý lại
toàn bộ lịch sử.

### 1.4 Partition — và trần của mức song song

**Partition** là đơn vị song song. Một topic chia thành N partition, và:

> **Một partition chỉ được giao cho tối đa một consumer trong cùng một group.**

Hệ quả rất cụ thể: topic có **1 partition** thì dù bạn scale consumer lên 3
bản, chỉ **1 bản làm việc**. Hai bản kia vào group, không được giao gì, ngồi
không và tốn RAM.

Muốn 3 consumer cùng chạy thì phải có ≥ 3 partition.

Và: **số partition chỉ tăng được, không giảm.** Tăng lên sẽ phân bố lại key
giữa các partition → thứ tự theo key bị phá với mọi key bị dời chỗ.

### 1.5 Consumer group và offset

Consumer cùng `group.id` chia nhau các partition. Mỗi group giữ offset riêng.

**Auto-commit là một cái bẫy.** Mặc định, client định kỳ commit offset nền:

```
nhận bản ghi  → auto-commit offset → bắt đầu xử lý → pod bị evict
                                                      ▲
                                            sự kiện MẤT VĨNH VIỄN
```

Đó là *at-most-once* đội lốt. Cách đúng: tắt auto-commit, **chỉ commit sau khi
xử lý xong**:

```
nhận bản ghi → xử lý → thành công → commit
                     → pod chết trước khi commit → gửi lại ở lần sau
```

Đó là **at-least-once**, và cái giá là handler phải **idempotent** — xử lý
cùng một sự kiện hai lần phải cho cùng kết quả.

### 1.6 Cái bẫy ghi kép

Đây là phần quan trọng nhất của bài.

```go
// Ghi database
INSERT INTO kyc_applications ...
COMMIT                          // ✓ thành công

// Phát sự kiện
producer.Publish(event)         // ✗ Kafka đang chết
```

Bản ghi tồn tại. Sự kiện **không bao giờ được gửi**. Không ai chấm điểm hồ sơ
đó, mãi mãi. Và không có log lỗi nào ở phía consumer vì chẳng có gì tới nơi.

Đảo thứ tự còn tệ hơn: phát trước, commit sau → `scoring` nhận sự kiện cho một
bản ghi **không tồn tại**.

Đây gọi là **vấn đề ghi kép (dual-write)**: hai hệ thống lưu trữ, không có
transaction chung.

Lời giải chuẩn là **transactional outbox**:

```sql
BEGIN;
  INSERT INTO kyc_applications (...);
  INSERT INTO outbox (topic, key, payload);   -- cùng transaction!
COMMIT;
```

Rồi một goroutine riêng quét bảng `outbox`, phát đi, và đánh dấu đã phát. Giờ
chỉ còn **một** lần ghi, nên hoặc cả hai cùng có hoặc cả hai cùng không.

> Repo này **cố ý để lộ lỗ hổng này** thay vì giấu. Lab 6 bắt bạn tự xây
> outbox. Việc nhìn thấy lỗi trước khi học cách sửa là có chủ đích.

### 1.7 Strimzi và KRaft

Strimzi là **operator** cho Kafka — đúng mẫu ở bài 09. Bạn viết CR, operator
lo phần còn lại.

Hai điều đã thay đổi gần đây mà mọi hướng dẫn cũ vẫn viết sai:

**ZooKeeper đã bị gỡ bỏ.** Kafka hiện dùng **KRaft** — tự quản metadata bằng
Raft. Strimzi 1.x từ chối `spec.zookeeper` và bắt buộc phải có `KafkaNodePool`.

**apiVersion đã đổi.** Strimzi 1.x phục vụ `kafka.strimzi.io/v1`, không phải
`v1beta2`. Dùng sai sẽ nhận `no matches for kind "Kafka"` **dù CRD rõ ràng đã
cài** — một thông báo cực kỳ đánh lạc hướng.

Kiểm tra bằng:

```bash
kubectl api-resources --api-group=kafka.strimzi.io
```

---

## 2. Trong repo này nằm đâu

### Operator

`deploy/argocd/platform/02-strimzi-operator.yaml`, cũng ở sync wave `-10` như
CNPG, và cũng vì cùng lý do.

### Cluster Kafka

`deploy/charts/kafka/templates/kafka.yaml:2`

```yaml
apiVersion: kafka.strimzi.io/v1
kind: Kafka
spec:
  kafka:
    version: {{ .Values.cluster.version }}
    listeners:
      - name: plain
        port: 9092
        type: internal
        tls: false
    config:
      offsets.topic.replication.factor: 1
      transaction.state.log.replication.factor: 1
      default.replication.factor: 1
      min.insync.replicas: 1
```

> Mọi `replication.factor` **phải là 1** trên cluster một broker. Để mặc định
> 3 thì các topic nội bộ không tạo được và cluster không bao giờ Ready — với
> một lỗi chẳng nhắc gì tới replication.

Đọc khối chú thích đầu file (dòng 3–13): nó ghi lại chính xác cái bẫy
apiVersion ở mục 1.7.

### Node pool

`deploy/charts/kafka/templates/nodepool.yaml:15`

```yaml
kind: KafkaNodePool
metadata:
  labels:
    strimzi.io/cluster: {{ .Values.cluster.name }}   # thiếu nhãn này -> pool mồ côi
spec:
  replicas: {{ .Values.nodePool.replicas }}
  roles:
    - controller      # chạy quorum Raft giữ metadata
    - broker          # lưu partition, phục vụ client
  storage:
    type: persistent-claim
    deleteClaim: false
  jvmOptions:
    -Xms: {{ .Values.nodePool.jvm.heap }}
    -Xmx: {{ .Values.nodePool.jvm.heap }}
```

Chú thích trong file giải thích vì sao gộp hai vai trò vào một node (tiết kiệm
~700 Mi so với tách hai pool) và vì sao heap chỉ bằng ~50% limit (Kafka dựa
nhiều vào page cache của OS chứ không phải heap).

### Topic

`deploy/charts/kafka/templates/topic.yaml:2`

```yaml
kind: KafkaTopic
spec:
  partitions: {{ .Values.topic.partitions }}
  replicas: {{ .Values.topic.replicas }}
  config:
    retention.ms: {{ .Values.topic.retentionMs | quote }}
    cleanup.policy: delete
```

Và `deploy/charts/kafka/values.yaml`:

```yaml
  retentionMs: "604800000"   # bọc nháy — xem chú thích ngay trên nó
  segmentBytes: "10485760"
```

> Đây là cái bẫy số lớn của bài 08, **đã thực sự xảy ra** khi dựng repo này.
> Chú thích trong file ghi lại cả thông báo lỗi.

### Producer

`internal/platform/events/events.go:39`

```go
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.DefaultProduceTopic(topic),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.RecordRetries(5),
	)
```

Và `internal/platform/events/events.go:59`, hàm `Publish` — dùng `ProduceSync`, nghĩa là **đồng bộ**:
HTTP handler gọi nó sẽ biết thật sự thành công hay không, thay vì nói dối
client về một lần ghi chưa hề xảy ra.

### Consumer

`internal/platform/events/events.go:120`

```go
	client, err := kgo.NewClient(
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topic),
		kgo.DisableAutoCommit(),          // <- mấu chốt
		kgo.SessionTimeout(20*time.Second),
	)
```

Và vòng lặp ở `internal/platform/events/events.go:148`:

```go
		fetches.EachRecord(func(rec *kgo.Record) {
			c.process(ctx, rec, handle)
		})

		if err := c.client.CommitUncommittedOffsets(ctx); err != nil {
```

Commit **sau** khi xử lý — chính là at-least-once ở mục 1.5.

Để ý thêm đoạn trong `Run` khi context bị huỷ: nó commit nốt những gì đã xong
*trước* khi thoát, nên pod thay thế không xử lý lại phần đó.

### Handler idempotent

`internal/scoring/scoring.go`, trong hàm `Handle`:

```go
	_, err := s.DB.Exec(ctx,
		`INSERT INTO credit_scores (...)
		 VALUES ($1, $2, $3, $4, $5, now())
		 ON CONFLICT (application_id) DO UPDATE
		   SET score = EXCLUDED.score, ...`,
```

`ON CONFLICT DO UPDATE` là cái giá của at-least-once. Không có nó, lần gửi lại
sẽ hỏng với lỗi khoá trùng.

### Lỗ hổng ghi kép — nhìn tận mắt

`internal/kyc/kyc.go`, hàm `submit`:

```go
	if err := s.insert(ctx, app); err != nil { ... 500 ... }

	// Publish SAU khi commit. Thứ tự ngược lại sẽ để scoring nhận sự kiện cho
	// một bản ghi không bao giờ tồn tại. Kiểu hỏng còn lại — bản ghi có, sự
	// kiện mất — chính là thứ transactional outbox giải quyết; xem lab 6.
	if err := s.Producer.Publish(ctx, app.ID, evt); err != nil {
		httpx.WriteJSON(w, http.StatusAccepted, map[string]any{
			"application": app,
			"warning":     "stored, but scoring was not notified",
		})
		return
	}
```

> Trả **202 chứ không phải 500** là một quyết định thiết kế: hồ sơ đã lưu bền,
> chỉ việc thông báo hỏng. Trả 500 sẽ mời client thử lại và tạo hồ sơ trùng.

### Trace đi xuyên Kafka

`internal/platform/events/carrier.go` — phần này thuộc bài 11, nhưng ghi nhớ
sự tồn tại của nó: nó là thứ làm một trace đi liền mạch qua chặng bất đồng bộ.

---

## 3. Thực hành

### 3.1 Nhìn cluster Kafka

```bash
kubectl -n kafka get kafka,kafkanodepool,kafkatopic
kubectl -n kafka get kafka kalapa -o jsonpath='{.status.conditions}' | jq
```

Thói quen "nhìn `.status`" từ bài 09 lại dùng được ngay.

### 3.2 Đọc topic trực tiếp

```bash
# Terminal 1
make kafka-console
```

```bash
# Terminal 2 — tạo một hồ sơ
kubectl -n ingress-nginx port-forward svc/ingress-nginx-controller 18080:80 &
curl -s -X POST -H 'Host: api.kalapa.local' -H 'Content-Type: application/json' \
  -d '{"national_id":"079111222333","full_name":"Hoc Kafka"}' \
  http://localhost:18080/api/kyc/applications | jq
```

Ở terminal 1 bạn thấy sự kiện JSON xuất hiện ngay. Đó là `kyc` nói chuyện với
`scoring` mà không hề gọi nó.

### 3.3 Chứng minh bất đồng bộ thật sự tách rời — bài quan trọng nhất

```bash
# Tắt hẳn consumer
kubectl -n kalapa scale deploy/scoring --replicas=0

# Gửi vài hồ sơ
for i in 1 2 3; do
  curl -s -X POST -H 'Host: api.kalapa.local' -H 'Content-Type: application/json' \
    -d "{\"national_id\":\"07955566600$i\",\"full_name\":\"Tach Roi $i\"}" \
    http://localhost:18080/api/kyc/applications | jq -r .id
done
```

**`kyc` vẫn trả 201 bình thường.** Với kiến trúc HTTP đồng bộ, cả ba request
này đã hỏng.

```bash
# Bật consumer trở lại
kubectl -n kalapa scale deploy/scoring --replicas=1
kubectl -n kalapa rollout status deploy/scoring
kubectl -n kalapa logs deploy/scoring | grep "application scored"
```

Cả ba được xử lý — chúng đã nằm chờ trong Kafka suốt thời gian đó.

### 3.4 Partition là trần của song song

```bash
kubectl -n kafka get kafkatopic kyc.application.submitted -o jsonpath='{.spec.partitions}'; echo
kubectl -n kalapa scale deploy/scoring --replicas=3
kubectl -n kalapa logs -l app.kubernetes.io/name=scoring --prefix --tail=30 | grep -i "partition\|consumer started"
```

Chỉ một pod được giao partition. Hai pod kia ngồi không.

```bash
# Tăng partition
kubectl -n kafka patch kafkatopic kyc.application.submitted \
  --type merge -p '{"spec":{"partitions":3}}'
sleep 15
./scripts/load.sh 30 5
kubectl -n kalapa logs -l app.kubernetes.io/name=scoring --prefix --tail=40 | grep -i partition
```

Giờ cả ba cùng làm việc.

```bash
# Dọn
kubectl -n kalapa scale deploy/scoring --replicas=1
```

### 3.5 Xem offset và lag

```bash
kubectl -n kafka run lag-$RANDOM --rm -it --restart=Never \
  --image=quay.io/strimzi/kafka:latest-kafka-4.3.1 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server kalapa-kafka-bootstrap:9092 \
  --describe --group scoring
```

Cột `LAG` là số sự kiện chưa xử lý. Đây là **con số quan trọng nhất** của một
hệ event-driven. Thử: scale `scoring` về 0, sinh tải, rồi chạy lại lệnh này.

### 3.6 Tự tay gây ra ghi kép

```bash
# Hạ Kafka
kubectl -n kafka scale statefulset kalapa-combined --replicas=0
sleep 20

# Gửi hồ sơ
curl -s -X POST -H 'Host: api.kalapa.local' -H 'Content-Type: application/json' \
  -d '{"national_id":"079999888777","full_name":"Ghi Kep"}' \
  http://localhost:18080/api/kyc/applications | jq
```

Bạn nhận **202** với `"warning": "stored, but scoring was not notified"`.

```bash
# Bật Kafka lại
kubectl -n kafka scale statefulset kalapa-combined --replicas=1
kubectl -n kafka rollout status statefulset kalapa-combined

# Hồ sơ có trong DB không?
kubectl -n data exec kalapa-db-1 -- psql -U kalapa -d kalapa -tAc \
  "SELECT id FROM kyc_applications WHERE national_id='079999888777';"

# Điểm có không?
kubectl -n data exec kalapa-db-1 -- psql -U kalapa -d kalapa -tAc \
  "SELECT count(*) FROM credit_scores WHERE national_id='079999888777';"
```

Hồ sơ **có**, điểm **bằng 0**. Sự kiện mất vĩnh viễn. Đây chính là lỗ hổng mà
lab 6 bắt bạn vá.

### 3.7 Idempotent trong thực tế

```bash
# Đọc lại topic từ đầu bằng một group MỚI, dùng chính group của scoring
kubectl -n kafka run reset-$RANDOM --rm -it --restart=Never \
  --image=quay.io/strimzi/kafka:latest-kafka-4.3.1 -- \
  bin/kafka-consumer-groups.sh --bootstrap-server kalapa-kafka-bootstrap:9092 \
  --group scoring --reset-offsets --to-earliest --topic kyc.application.submitted --execute
```

Lệnh này báo lỗi nếu consumer đang chạy (nhóm đang hoạt động). Hãy:

```bash
kubectl -n kalapa scale deploy/scoring --replicas=0
# chạy lại lệnh reset ở trên
kubectl -n kalapa scale deploy/scoring --replicas=1
kubectl -n kalapa logs -f deploy/scoring
```

`scoring` xử lý lại **toàn bộ lịch sử**. Nhờ `ON CONFLICT DO UPDATE`, kết quả
giống hệt — không bản ghi trùng, không lỗi. Đó là ý nghĩa của idempotent.

```bash
kill %1 2>/dev/null
```

---

## 4. Tự kiểm

- [ ] Năm vấn đề của gọi HTTP đồng bộ giữa hai service?
- [ ] Hướng sự kiện đánh đổi cái gì để lấy sự tách rời?
- [ ] Kafka khác hàng đợi truyền thống ở bốn điểm nào?
- [ ] Partition là gì? Vì sao nó là trần của mức song song?
- [ ] Số partition có giảm được không? Tăng thì phải trả giá gì?
- [ ] Vì sao auto-commit nguy hiểm? Nó cho ngữ nghĩa gì?
- [ ] At-least-once đòi hỏi handler phải thế nào?
- [ ] Mô tả vấn đề ghi kép. Đảo thứ tự có cứu được không?
- [ ] Transactional outbox giải quyết nó bằng cách nào?
- [ ] Vì sao `kyc` trả 202 chứ không phải 500 khi publish hỏng?
- [ ] Vì sao mọi `replication.factor` phải là 1 ở lab này?
- [ ] Strimzi 1.x phục vụ apiVersion nào? Dùng sai thì lỗi trông ra sao?

---

## 5. Bẫy thường gặp

| Bẫy | Thực tế |
|---|---|
| Dùng HTTP đồng bộ cho mọi thứ | Lỗi lan truyền, ràng buộc chặt |
| Để auto-commit bật | Mất sự kiện khi pod bị evict giữa chừng |
| Handler không idempotent | At-least-once sẽ tạo bản ghi trùng |
| Scale consumer vượt số partition | Pod thừa ngồi không, tốn RAM |
| Phát sự kiện trước khi commit DB | Consumer nhận sự kiện cho bản ghi không tồn tại |
| Bỏ qua vấn đề ghi kép | Mất dữ liệu âm thầm, không log nào báo |
| Dùng `v1beta2` với Strimzi 1.x | `no matches for kind` dù CRD đã cài |
| Để `replication.factor: 3` trên 1 broker | Cluster không bao giờ Ready, lỗi không nhắc gì tới replication |
| Không theo dõi consumer lag | Không biết hệ thống đang tụt lại cho tới khi quá muộn |

---

**Bài tiếp:** [11 — Observability](11-observability.md)
