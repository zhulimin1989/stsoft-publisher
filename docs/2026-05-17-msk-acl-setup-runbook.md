# MSK SCRAM ACL 接入 Runbook(跨 VPC 场景)

**日期**:2026-05-17
**适用集群**:`tokengateway-event-cluster`(us-east-1,vpc1)
**适用场景**:vpc2 应用通过 SASL/SCRAM 接入 MSK 消费/生产事件
**当前接入方**:`codesphere-billing-api`(vpc2 消费端) + `gateway`(vpc1 生产端)

---

## 1. 架构现状

```
vpc1                                        vpc2 (10.0.0.0/16)
┌──────────────────┐                       ┌──────────────────────────┐
│ MSK Cluster      │                       │ EC2 10.0.12.152          │
│ tokengateway-    │ ◄── PrivateLink ──────┤  billing-api 容器        │
│ event-cluster    │   (Multi-VPC SCRAM)   │  SCRAM consumer          │
│                  │                       │                          │
│ Brokers          │                       │ Endpoint ENIs(Multi-VPC):│
│ 14001-14003      │                       │  10.0.74.66 :14001       │
│                  │                       │  10.0.37.210:14002       │
│ Bootstrap        │                       │  10.0.60.118:14003       │
│ Multi-VPC SCRAM: │                       │  ← SG 必须开 14001-14100 │
│ 14098-14100      │                       │    inbound               │
└──────────────────┘                       └──────────────────────────┘

vpc1 内 IAM 管理通道(用完即删):
┌────────────────────────────────────────┐
│ 临时 EC2(t3.micro)                    │
│ Role: ec2-kafka-acl-admin              │
│ 用 IAM 身份跑 kafka-acls.sh 管 ACL    │
│ Docker 镜像:kafka-iam:local            │
│  (apache/kafka + aws-msk-iam-auth JAR) │
└────────────────────────────────────────┘
```

### 关键拓扑参数

| 项 | 值 |
|---|---|
| Cluster ARN | `arn:aws:kafka:us-east-1:654654182678:cluster/tokengateway-event-cluster/358498c3-0d9e-4910-8bc4-b9e19649520d-10` |
| 鉴权模式 | SASL/SCRAM + IAM(共存,Multi-Auth)|
| Multi-VPC SCRAM bootstrap | `BootstrapBrokerStringVpcConnectivitySaslScram`(`boot-...:14098-14100`)|
| Multi-VPC SCRAM 已暴露给 | vpc2(`10.0.0.0/16`)|
| IAM bootstrap | `BootstrapBrokerStringSaslIam`(单 VPC,仅 vpc1 可达)|
| 集群侧 broker 配置 | `auto.create.topics.enable=true` / `num.partitions=3` / **`allow.everyone.if.no.acl.found=false`** |

---

## 2. 为什么是这套设计:3 个 AWS 硬约束

| 设想中的捷径 | 被挡住的原因 |
|---|---|
| 把 `allow.everyone.if.no.acl.found=true`,免 ACL 接入 | AWS 拒绝:**Multi-VPC SASL/SCRAM 启用时,此属性强制为 false**。报错:`The allow.everyone.if.no.acl.found configuration setting must be set to false.` |
| 在 MSK Configuration 加 `super.users=User:xxx`,做 super user 管 ACL | AWS 拒绝:**`super.users` 不在 MSK 客户可配置属性 allowlist**。报错:`Key 'super.users' is not supported by at least one Apache Kafka version.` |
| 让某个 SCRAM 用户绕过 ACL 直连 | 不存在的概念,SCRAM 客户端必走 ACL |

**唯一可行路径**:**额外启用 IAM 鉴权(与 SCRAM 共存)**,用 IAM 身份(角色策略上的 `kafka-cluster:AlterCluster` 等同于 super.users 效果)管理 ACL,SCRAM 业务通道照常使用。

---

## 3. 当前已落地的状态

### 3.1 集群侧

- ✅ `Sasl.Scram.Enabled = true`(业务接入)
- ✅ `Sasl.Iam.Enabled = true`(管理接入)
- ✅ Multi-VPC SCRAM PrivateLink 已暴露给 vpc2
- ⚠️ Multi-VPC IAM **未启用**(IAM 只在 vpc1 单 VPC 端点可达——管理操作必须在 vpc1 内做)

### 3.2 SCRAM 用户

- `tokenplatform_kafka_user`:gateway 写 + billing-api 读共用
- `tokenplatform_msk_admin`:为 super.users 路径建的,**当前未使用**(可保留备用,也可删)

### 3.3 IAM 资源

- **IAM Role**:`ec2-kafka-acl-admin`
- **Inline policy** `kafka-acl-admin`:
  ```json
  {
    "Version": "2012-10-17",
    "Statement": [
      {
        "Effect": "Allow",
        "Action": [
          "kafka:GetBootstrapBrokers",
          "kafka:DescribeCluster"
        ],
        "Resource": "arn:aws:kafka:us-east-1:654654182678:cluster/tokengateway-event-cluster/*"
      },
      {
        "Effect": "Allow",
        "Action": [
          "kafka-cluster:Connect",
          "kafka-cluster:AlterCluster",
          "kafka-cluster:DescribeCluster",
          "kafka-cluster:DescribeTopic",
          "kafka-cluster:DescribeGroup"
        ],
        "Resource": "arn:aws:kafka:us-east-1:654654182678:cluster/tokengateway-event-cluster/*"
      }
    ]
  }
  ```

### 3.4 现存 ACL(`User:tokenplatform_kafka_user`)

| Resource | Operations |
|---|---|
| Topic: `gateway.error_events` | Read + Write + Describe |
| Topic: `gateway.ops_events` | Read + Write + Describe |
| Topic: `gateway.process_events` | Read + Write + Describe |
| Topic: `gateway.request_events` | Read + Write + Describe |
| Topic: `gateway.usage_events` | Read + Write + Describe |
| Group: `tokenplatform_event` | Read |

### 3.5 网络通路

- ✅ vpc2 EC2 ↔ Multi-VPC SCRAM bootstrap(14098-14100):TCP 通,Private DNS 解析正常
- ✅ vpc2 EC2 ↔ broker advertised endpoints(14001-14003):TCP 通(SG 已开放),SCRAM SASL 握手成功
- ✅ `kafka-broker-api-versions.sh` 用 SCRAM 凭证拿到 3 个 broker 完整 API 列表

---

## 4. SOP:再有新 consumer / topic 要接入

适用场景:**新 service 用 SCRAM 接入,需要给 `<new-user>` 加 topic/group 权限**。

### Step 1 — SCRAM 用户(如果新建)

1. AWS Secrets Manager 建一个 secret,**name 必须以 `AmazonMSK_` 开头**,使用**客户管理的 KMS key**(不能用默认的 `aws/secretsmanager`)
2. Secret value:`{"username":"<service>_kafka_user","password":"<强随机>"}`
3. 关联到集群:
   ```bash
   aws kafka batch-associate-scram-secret \
     --cluster-arn "$CLUSTER_ARN" \
     --secret-arn-list "arn:aws:secretsmanager:us-east-1:...:secret:AmazonMSK_xxx"
   ```
4. 期望输出 `UnprocessedScramSecrets: []`(全成功)

### Step 2 — 启动临时 vpc1 EC2

如果之前的 `temp-kafka-acl-runner` 已 terminate,需要重新起。要点:

- VPC: **vpc1**(MSK 所在 VPC)
- Subnet: vpc1 内任意公网 subnet(为 docker pull / GitHub 出 internet)
- IAM instance profile: **`ec2-kafka-acl-admin`**
- Security group: inbound 只开 SSH 22 / Source = My IP
- 文件系统: **无**
- AMI: Ubuntu 22.04 / Amazon Linux 2023 都行

启动后用 EC2 Instance Connect 或 ssh 进去。

### Step 3 — 准备 docker + 镜像

```bash
# Docker
sudo apt update -qq && sudo apt install -y -qq docker.io unzip curl
sudo usermod -aG docker $USER && newgrp docker

# AWS CLI v2
curl -s "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o /tmp/aws.zip
unzip -q /tmp/aws.zip -d /tmp/ && sudo /tmp/aws/install
aws sts get-caller-identity   # 验证 role 附上了

# Docker 镜像
mkdir -p ~/kafka-iam && cd ~/kafka-iam
cat > Dockerfile <<'EOF'
FROM apache/kafka:latest
USER root
RUN apk add --no-cache curl ca-certificates && \
    curl -fL -o /opt/kafka/libs/aws-msk-iam-auth-2.2.0-all.jar \
      https://github.com/aws/aws-msk-iam-auth/releases/download/v2.2.0/aws-msk-iam-auth-2.2.0-all.jar
EOF
docker build -t kafka-iam:local .

# IAM properties
cat > ~/iam.properties <<'EOF'
security.protocol=SASL_SSL
sasl.mechanism=AWS_MSK_IAM
sasl.jaas.config=software.amazon.msk.auth.iam.IAMLoginModule required;
sasl.client.callback.handler.class=software.amazon.msk.auth.iam.IAMClientCallbackHandler
EOF
chmod 600 ~/iam.properties

# Bootstrap
CLUSTER_ARN="arn:aws:kafka:us-east-1:654654182678:cluster/tokengateway-event-cluster/358498c3-0d9e-4910-8bc4-b9e19649520d-10"
BOOTSTRAP=$(aws kafka get-bootstrap-brokers \
  --cluster-arn "$CLUSTER_ARN" \
  --query 'BootstrapBrokerStringSaslIam' --output text)
```

### Step 4 — 验证 IAM 通路

```bash
docker run --rm --network host \
  -v ~/iam.properties:/iam.properties:ro \
  -e AWS_REGION=us-east-1 \
  kafka-iam:local \
  /opt/kafka/bin/kafka-acls.sh \
    --bootstrap-server "$BOOTSTRAP" \
    --command-config /iam.properties \
    --list
```

期望:返回 ACL 列表(空或非空都行,**不报 ClusterAuthorizationException**)。

### Step 5 — 加 ACL

```bash
PRINCIPAL="User:<new-user>"
GROUP="<new-consumer-group>"
TOPICS=( <topic-1> <topic-2> ... )

kacls() {
  docker run --rm --network host \
    -v ~/iam.properties:/iam.properties:ro \
    -e AWS_REGION=us-east-1 \
    kafka-iam:local \
    /opt/kafka/bin/kafka-acls.sh --bootstrap-server "$BOOTSTRAP" \
      --command-config /iam.properties "$@"
}

# 消费者权限(读)
for t in "${TOPICS[@]}"; do
  kacls --add --allow-principal "$PRINCIPAL" \
    --operation Read --operation Describe --topic "$t"
done

# 生产者权限(如同一账号也写)
for t in "${TOPICS[@]}"; do
  kacls --add --allow-principal "$PRINCIPAL" \
    --operation Write --topic "$t"
done

# Consumer group
kacls --add --allow-principal "$PRINCIPAL" \
  --operation Read --group "$GROUP"

# 验证
kacls --list --principal "$PRINCIPAL"
```

### Step 6 — vpc2 端 PrivateLink Endpoint SG

新 consumer 在 vpc2 上跑的话,**默认 SG 不开 14001-14100**,要手动加:

1. AWS Console → **VPC → Endpoints**
2. 找 vpc2 内 Type=Interface、Service name 含 `vpce-svc-` 的 kafka 端点
3. **Security groups tab → 看挂的 SG ID**
4. 跳到 **Security Groups** 找该 SG → **Inbound rules → Edit**
5. 添加:
   ```
   Type: Custom TCP
   Port range: 14001-14100
   Source: 10.0.0.0/16(vpc2 CIDR)或 EC2 SG ID
   ```
6. Save

注意:**`14001-14003` 是 broker advertised,`14098-14100` 是 bootstrap,一起放行最稳妥**。

### Step 7 — 业务侧验证(vpc2 EC2 / 容器内)

```bash
# SCRAM properties
cat > ~/scram.properties <<EOF
security.protocol=SASL_SSL
sasl.mechanism=SCRAM-SHA-512
sasl.jaas.config=org.apache.kafka.common.security.scram.ScramLoginModule required username="<user>" password="<pass>";
EOF
chmod 600 ~/scram.properties

# Multi-VPC SCRAM bootstrap
SCRAM_BOOTSTRAP=$(aws kafka get-bootstrap-brokers \
  --cluster-arn "$CLUSTER_ARN" \
  --query 'BootstrapBrokerStringVpcConnectivitySaslScram' --output text)

# 测连通(返回 broker API 列表 = OK)
docker run --rm --network host \
  -v ~/scram.properties:/scram.properties:ro \
  apache/kafka:latest \
  /opt/kafka/bin/kafka-broker-api-versions.sh \
    --bootstrap-server "$SCRAM_BOOTSTRAP" \
    --command-config /scram.properties
```

### Step 8 — 清理

- **Terminate 临时 vpc1 EC2**(避免长挂特权 instance)
- 保留 IAM Role `ec2-kafka-acl-admin`(下次复用)
- 保留 SCRAM admin secret(`AmazonMSK_*`)只是关联到集群,不再使用,删不删都行

---

## 5. 常见错误与排查

| 错误 | 含义 | 修法 |
|---|---|---|
| `TopicAuthorizationException` | 该 topic 缺 ACL | 回 Step 5 加 |
| `GroupAuthorizationException` | group 缺 Read ACL | 回 Step 5 加;若先前看似已加,见"案例复盘 2026-05-18" |
| `KafkaJSGroupCoordinatorNotFound: Failed to find group coordinator` | **KafkaJS 把 group auth 失败误映射成 "coordinator not found"**;90% 是 group ACL 问题而非网络 | 当作 `GroupAuthorizationException` 处理,见"案例复盘 2026-05-18" |
| `ClusterAuthorizationException: idempotent send` | producer 用了 idempotent 模式但缺 `IdempotentWrite on Cluster` | 加 `kacls --add ... --operation IdempotentWrite --cluster` |
| `TransactionalIdAuthorizationException` | producer 用了 transactional 模式 | 加 `--operation Write --transactional-id <id>` |
| `Timed out waiting to send the call` | TCP 通但 broker 拒绝/超时,或 advertised broker 不通 | 检查 SG inbound 14001-14003 是否开,见 Step 6 |
| `Connection timed out`(nc 测试)| 包到不了 ENI,SG 在挡 | 同上 |
| nslookup `NXDOMAIN`(在 vpc2 内) | Private DNS 没启用 | VPC Endpoint → Modify private DNS → Enable |
| `AccessDeniedException: kafka:GetBootstrapBrokers` | EC2 role 缺权限 | 加到 inline policy 或从 CloudShell 取值后粘贴 |

---

## 案例复盘:2026-05-18 Group ACL silent-failure

### 问题表现

- billing-api(`codesphere-billing-api`)启动后消费 Kafka 报错:
  ```
  KafkaJSGroupCoordinatorNotFound: Failed to find group coordinator
  Not authorized to access group: Group authorization failed
  ```
- 同样症状在 vpc2 EC2 上手工复现(用 billing-api 同款 SCRAM 凭证 + console-consumer):
  ```
  org.apache.kafka.common.errors.GroupAuthorizationException:
      Not authorized to access group: tokenplatform_event
  ```
- topic 类 ACL 看起来正常(`gateway.request_events` 等 5 个 topic 没报 `TopicAuthorizationException`)。

### 根因分析过程

按"从近到远"的隔离顺序排查:

1. **client 配置层**:`docker exec ... env | grep BILLING_USAGE_KAFKA_GROUP_ID` → `tokenplatform_event`,**配置侧无问题**
2. **client 运行时**:`docker logs ... | grep "Kafka usage consumer started"` 显示 `group=tokenplatform_event`,**容器已重启读到新 env,排除"改了没重启"**
3. **复现到客户端无关层**:vpc2 EC2 上直接用 `kafka-console-consumer.sh` 拿 billing-api 的 SCRAM 用户密码消费同一 group → **复现同样错误**,**确认非 billing-api 代码 bug**
4. **核实 SCRAM 身份**:`docker exec ... printenv BILLING_USAGE_KAFKA_USERNAME` = `tokenplatform_kafka_user`,跟 ACL 加给的 principal 一致,**排除身份错配**
5. **看 ACL 实物**:vpc1 临时 EC2 上 `kafka-acls.sh --list --principal "User:tokenplatform_kafka_user"`,输出 **5 个 TOPIC × Read+Write+Describe ACL,但没有任何 GROUP ACL** → **定位完毕**

KafkaJS 那条 `KafkaJSGroupCoordinatorNotFound` 是 `GroupAuthorizationException` 的间接现象:client 调 `FindCoordinator` API 找 group 协调器,broker 在 ACL 检查阶段拒绝,KafkaJS 把这种拒绝映射成了 "coordinator not found",**误导性较强**,容易让人以为是网络问题。

### 根因

**前一天加 group ACL 的命令静默失败,group ACL 从未真正落到 broker 上**。

机制:`kafka-acls.sh --add --bootstrap-server "$BOOTSTRAP" ...` 在 `$BOOTSTRAP` 变量为空时,某些版本的 kafka-acls.sh 会照常输出:

```
Adding ACLs for resource `ResourcePattern(resourceType=GROUP, ...)`: 
        (principal=User:..., host=*, operation=READ, permissionType=ALLOW)
```

**但不真实下发到 broker**——属于 client 端的 silent failure(应该报错却没报)。

当时的具体场景:shell session 经过一次重连(EC2 Instance Connect 浏览器 tab 刷新),环境变量 `$BOOTSTRAP` 丢失而未察觉。5 个 topic ACL 是在 BOOTSTRAP 还在的 for 循环里批量加的所以都成功了;group ACL 是单独一条命令,正好撞上变量丢失的时刻。

### 解决方案

ssh 到 vpc1 临时 EC2,先确认 BOOTSTRAP 非空,然后重新加:

```bash
# 1. 重新 set bootstrap(shell 重连后必做)
BOOTSTRAP=$(aws kafka get-bootstrap-brokers \
  --cluster-arn "$CLUSTER_ARN" \
  --query 'BootstrapBrokerStringSaslIam' --output text)
[ -z "$BOOTSTRAP" ] && { echo "BOOTSTRAP empty, abort"; exit 1; }

# 2. 补 group ACL
kacls --add --allow-principal "User:tokenplatform_kafka_user" \
  --operation Read --group "tokenplatform_event"

# 3. 立即 list 验证(关键步骤,不可省)
kacls --list --principal "User:tokenplatform_kafka_user"
```

### 结果验证

3 步对账,缺一不可:

1. **vpc1 EC2**:`kacls --list --principal ...` 输出应包含
   ```
   Current ACLs for resource `ResourcePattern(resourceType=GROUP, name=tokenplatform_event, patternType=LITERAL)`:
           (principal=User:tokenplatform_kafka_user, host=*, operation=READ, permissionType=ALLOW)
   ```
   **总条数 = 6**(5 topic 段 + 1 group 段)
2. **vpc2 EC2 console-consumer**:
   ```
   docker run --rm --network host -v /tmp/scram.properties:/scram.properties:ro \
     apache/kafka:latest /opt/kafka/bin/kafka-console-consumer.sh \
     --bootstrap-server "$SCRAM_BOOTSTRAP" --command-config /scram.properties \
     --group tokenplatform_event --topic gateway.request_events \
     --max-messages 1 --timeout-ms 30000
   ```
   预期输出 `TimeoutException ... Processed a total of 0 messages`——**关键是没有任何 `GroupAuthorizationException` / `TopicAuthorizationException`**。timeout 0 messages 不是错,只是 topic 当下没数据。
3. **billing-api 重启**:`docker restart codesphere-billing-api`,启动日志显示
   ```
   Kafka usage consumer started topics=gateway.request_events,... brokers=3 group=tokenplatform_event
   ```
   **后续日志中没有任何 `KafkaJS*Exception` / `GroupAuthorization` 字样**。

### 防御建议(避免下次重蹈覆辙)

1. **所有 kafka-acls.sh 操作前守卫 `$BOOTSTRAP` 非空**:
   ```bash
   [ -z "$BOOTSTRAP" ] && { echo "BOOTSTRAP empty, abort"; exit 1; }
   ```
2. **加完 ACL 必须 `--list --principal` 列出来肉眼对账**——不要只看 "Adding ACLs..." 的回显
3. **shell 重连后第一件事是检查关键变量**:`echo BOOTSTRAP=$BOOTSTRAP CLUSTER_ARN=$CLUSTER_ARN`,任一为空都重新 set
4. **后续可考虑包一个**`kacls_add_and_verify` 函数,接受参数,内部加完后立刻 list 比对,缺失就 retry——把"加完一定验证"做成肌肉记忆
5. **KafkaJS 报 `GroupCoordinatorNotFound` 时,90% 是 group ACL 而非真的网络问题**——记住这条映射关系,排查时先查 ACL 再查网络

---

## 6. 教训(写给以后的自己)

1. **MSK Multi-VPC + 严格 ACL 绑死**——开了 Multi-VPC 就吃 ACL 管理成本,不能避免
2. **MSK SCRAM 集群没有"开箱即用" admin**,必须开 IAM auth 作为管理后门
3. **PrivateLink Endpoint SG 跟 EC2 SG 完全独立**,默认只开 443,跨 VPC 接其他端口必须手动改 endpoint 自身的 SG
4. **Bootstrap 端口(14098-14100)和 advertised broker 端口(14001-14003)走同一条 PrivateLink 但端口不同**,SG 要把 14001-14100 整段放行,光开 bootstrap 不够
5. **kafka-acls.sh 用 IAM auth 需要 `aws-msk-iam-auth` JAR**,放进镜像或本地 Kafka 安装的 `libs/`
6. **`bitnami/kafka:latest` 已不存在**(Bitnami 在 2025 年改了发布策略),用 `apache/kafka:latest` 替代;路径 `/opt/kafka/bin/kafka-acls.sh`,Alpine 基础,需 `apk add curl`
7. **Multi-VPC SCRAM 端点(boot-XXX)和 advertised broker(b-N.scram.XXX)的端口是设计上不同的端口范围**,Multi-VPC bootstrap 走 PrivateLink 转发的 NLB 端口,broker 通信也走 PrivateLink 但 advertised hostname 不同;Private DNS 必须启用才能在 vpc2 内解析 broker hostname
8. **CloudShell 能做 AWS API 操作**(create-config / update-security 等),**但不能跑 kafka-acls.sh**(它在 AWS 托管 VPC 里,不能跨 VPC 到 MSK)——所以才需要 vpc1 临时 EC2
9. **`kafka-acls.sh --add` 在 `--bootstrap-server ""`(空字符串)时是 silent failure**:屏幕回显 "Adding ACLs..." 看似成功,**实际没下发到 broker**。每次 shell 重连(EC2 Instance Connect 浏览器刷新等)环境变量会丢,**操作前必须先 `echo $BOOTSTRAP` 验证非空 + 操作后 `--list` 对账**。2026-05-18 那次故障的根因就是这个,见"案例复盘"
10. **KafkaJS 的 `GroupCoordinatorNotFound` ≠ 网络问题**:这是 KafkaJS 对 broker 端 `GROUP_AUTHORIZATION_FAILED` 的不友好映射。看到这个错先查 group ACL,不要先查 DNS / SG

---

## 7. 待办(2026-05-17 留)

- [ ] 改 billing-api 配置:
  - `BOOTSTRAP_SERVERS` = Multi-VPC SCRAM bootstrap
  - `KAFKA_TOPICS` 加 `gateway.` 前缀 + 复数 `_events`
  - `KAFKA_GROUP_ID` = `tokenplatform_event`
  - `BILLING_USAGE_KAFKA_ENABLED=true`
- [ ] 重启 billing-api 验证无 auth error
- [ ] Terminate `temp-kafka-acl-runner`(`i-009c0e88839ea6ef3`)
- [ ] 如果 gateway 用 idempotent producer,加 `IdempotentWrite on Cluster` ACL(看启动日志再判断)
- [ ] 未来考虑拆分 gateway / billing-api 各自独立 SCRAM 用户(least-privilege)

---

## 附录:命令快速查找

```bash
# 查看集群状态
aws kafka describe-cluster --cluster-arn "$CLUSTER_ARN"

# 查看认证模式
aws kafka describe-cluster --cluster-arn "$CLUSTER_ARN" \
  --query 'ClusterInfo.ClientAuthentication.Sasl'

# 查看所有 bootstrap 字符串
aws kafka get-bootstrap-brokers --cluster-arn "$CLUSTER_ARN"

# 查看关联的 SCRAM secret
aws kafka list-scram-secrets --cluster-arn "$CLUSTER_ARN"

# 查看 broker 配置 revision
aws kafka describe-cluster --cluster-arn "$CLUSTER_ARN" \
  --query 'ClusterInfo.CurrentBrokerSoftwareInfo'

# 拉出 broker 实际配置内容
aws kafka describe-configuration-revision \
  --arn "$CONFIG_ARN" --revision "$CONFIG_REV" \
  --query 'ServerProperties' --output text | base64 -d
```
