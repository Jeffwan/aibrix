# kvcached & vLLM Sleep Mode 真机测试指南(Lambda Cloud)

> 目的:在动 AIBrix 控制面之前,**先独立验证两个上游原子能力**——kvcached(弹性 KV)与 vLLM sleep mode(整模型权重卸载)。本指南所有命令均在 Lambda Cloud A10(us-east-1,$1.29/hr)上真实跑通过;"上次实测"列的是 2026-06 我们的实际数字,可作为你复测时的参照基线。
>
> 建议整场测试控制在 1~2 小时内(≈$1.3~2.6),测完立即销毁实例(见 §9)。

## 0. 测试矩阵与预期结论

| # | 原子能力 | 测试 | 上次实测(A10 23GB) |
|---|---|---|---|
| T1 | kvcached 单引擎基线 | 起 1 个 kvcached 引擎,serve 一次 completion | 引擎就绪 ~50-63s(含下载 ~6.3s + compile);正常出词 |
| T2 | **单卡多模型**(核心) | 同卡起 2 个引擎(独立端口/IPC),各自 serve | 两模型共存 ~6.5GiB,互不干扰,2/2 出词 |
| T3 | KV 预算控制 | `kvctl limit` 调某模型 KV 上限 | 引擎 100ms 轮询后自动 resize |
| T4 | `/dev/shm` 协议直读 | 按 MemInfoStruct 布局解析预算段 | 3×int64 LE:total/used/prealloc,真实数字 |
| T5 | sleep level 1 | `/sleep?level=1` → 显存下降;`/wake_up` 恢复 | 0.6B 模型掉 ~1.5GiB;wake 亚秒~数秒 |
| T6 | level 1 vs level 2 | 对比两档 wake 时延 | level1(DRAM 回拷)≪ level2(磁盘重载) |
| T7 | kvcached × sleep 协同 | 双模型下 evict 一个,看另一个的 KV headroom | 空出的 HBM 立即可被另一模型的 KV 借用 |

两能力的关系(review 时的心智模型):**正交**。kvcached 管"多个活跃模型的 KV 弹性共存"(/dev/shm 观测,phase-2 可用 kvctl 调预算);sleep 管"空闲模型把权重吐出 HBM"(level 1→pinned DRAM,level 2→丢弃)。phase-1 只保留 kvcached launch/KV 观测和 vLLM sleep launch flags;控制面驱动 warm/evict 移入 phase-2。

---

## 1. Lambda Cloud 开机

前提:`export LAMBDA_API_KEY=<你的key>`(dashboard → API keys;**不要写进任何文件/仓库**)。

```bash
# 1) 查 A10 容量(哪些 region 有货)
curl -su "$LAMBDA_API_KEY:" https://cloud.lambdalabs.com/api/v1/instance-types \
  | python3 -c 'import sys,json; d=json.load(sys.stdin)["data"]["gpu_1x_a10"]; print(d["instance_type"]["price_cents_per_hour"]/100, [r["name"] for r in d["regions_with_capacity_available"]])'

# 2) 生成并注册一把临时 ssh key(测完删除)
ssh-keygen -t ed25519 -f /tmp/lambda_test -N '' -C kvcached-test
curl -su "$LAMBDA_API_KEY:" https://cloud.lambdalabs.com/api/v1/ssh-keys \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"kvcached-test\",\"public_key\":\"$(cat /tmp/lambda_test.pub)\"}"

# 3) 开机(记下返回的 instance_id)
curl -su "$LAMBDA_API_KEY:" https://cloud.lambdalabs.com/api/v1/instance-operations/launch \
  -H 'Content-Type: application/json' \
  -d '{"region_name":"us-east-1","instance_type_name":"gpu_1x_a10","ssh_key_names":["kvcached-test"],"name":"kvcached-sleep-test"}'

# 4) 轮询到 active + 拿 IP(约 3-5 分钟)
watch -n 15 "curl -su \"$LAMBDA_API_KEY:\" https://cloud.lambdalabs.com/api/v1/instances | python3 -m json.tool | grep -E '\"status\"|\"ip\"'"

# 5) 登录
ssh -i /tmp/lambda_test ubuntu@<IP>
nvidia-smi --query-gpu=name,memory.total --format=csv,noheader   # → NVIDIA A10, 23028 MiB
```

---

## 2. 环境准备(两条路线)

### 路线 A:预构建镜像(推荐,~10 分钟,我们验证走的这条)

Lambda 实例自带 docker + nvidia runtime,直接用 kvcached 官方引擎镜像(内含 python3.12 + vLLM 0.19.x + kvcached + kvctl/kvtop):

```bash
docker pull ghcr.io/ovg-project/kvcached-vllm:latest    # ~22GB,几分钟
docker run --rm -it --gpus all --shm-size=16g --network host \
  -v $HOME/hf-cache:/root/.cache/huggingface \
  ghcr.io/ovg-project/kvcached-vllm:latest bash
# 后续 T1-T7 全部在这个容器内执行
```

要点:`--shm-size=16g`(kvcached 预算段 + PyTorch IPC 都走 /dev/shm);`-v hf-cache`(权重缓存挂宿主机,重开容器不重下)。

### 路线 B:装进 venv(想测特定 vLLM 版本时用)

```bash
git clone https://github.com/ovg-project/kvcached && cd kvcached
cd engine_integration/scripts && ./setup.sh    # = pip install -e . + 对应版本引擎补丁
```
支持 vLLM ≥0.8.4、SGLang ≥0.4.9。B200/B300 用 `setup_b200.sh`。

---

## 3. T1/T2 — 单卡单模型 → 单卡双模型

另开一个宿主机终端持续观测显存(整场测试都留着):

```bash
nvidia-smi --query-gpu=memory.used,memory.total --format=csv,noheader -l 2
```

容器内起**模型 A**(这条命令与我们 runtime `SubprocessEngineLauncher` 生成的完全一致):

```bash
ENABLE_KVCACHED=true KVCACHED_AUTOPATCH=1 KVCACHED_IPC_NAME=kvc_m1 VLLM_SERVER_DEV_MODE=1 \
python3.12 -m vllm.entrypoints.openai.api_server \
  --model Qwen/Qwen3-0.6B --served-model-name qwen3-0.6b \
  --host 0.0.0.0 --port 20000 --enable-sleep-mode > /tmp/m1.log 2>&1 &

# 等就绪(首启:下载 ~6s + 引擎 boot/compile ~50s)
until curl -sf localhost:20000/health >/dev/null; do sleep 3; echo -n .; done; echo READY
```

三个必须记住的开关:
- `KVCACHED_IPC_NAME` **每模型必须唯一**(默认按 PGID 命名,多进程会撞段);
- `VLLM_SERVER_DEV_MODE=1` + `--enable-sleep-mode` 二者**同时**存在才有 `/sleep` `/wake_up` 端点;
- 开 kvcached 后**不要**再传 `--gpu-memory-utilization`(KV 由 kvcached 弹性管理)。

**T1 验证**:
```bash
curl -s localhost:20000/v1/chat/completions -H 'Content-Type: application/json' \
  -d '{"model":"qwen3-0.6b","messages":[{"role":"user","content":"say hi"}],"max_tokens":16}'
# 期望:HTTP 200 + 正常内容;记录此刻 memory.used 作为单模型基线
```

**T2:同卡再起模型 B**(只有端口和 IPC 名不同):
```bash
ENABLE_KVCACHED=true KVCACHED_AUTOPATCH=1 KVCACHED_IPC_NAME=kvc_m2 VLLM_SERVER_DEV_MODE=1 \
python3.12 -m vllm.entrypoints.openai.api_server \
  --model Qwen/Qwen2.5-0.5B-Instruct --served-model-name qwen2.5-0.5b \
  --host 0.0.0.0 --port 20001 --enable-sleep-mode > /tmp/m2.log 2>&1 &
until curl -sf localhost:20001/health >/dev/null; do sleep 3; done

# 两个都能出词、显存为两者之和(上次:~6.5GiB)
curl -s localhost:20000/v1/chat/completions -H 'Content-Type: application/json' -d '{"model":"qwen3-0.6b","messages":[{"role":"user","content":"1+1?"}],"max_tokens":8}'
curl -s localhost:20001/v1/chat/completions -H 'Content-Type: application/json' -d '{"model":"qwen2.5-0.5b","messages":[{"role":"user","content":"1+1?"}],"max_tokens":8}'
```

---

## 4. T3/T4 — KV 预算(kvctl)与 /dev/shm 协议直读

```bash
kvctl list          # 期望看到 kvc_m1、kvc_m2 两段及各自用量
kvtop               # nvtop 风格的每模型 KV 实时视图(Ctrl-C 退出)

kvctl limit kvc_m2 1G          # 把 B 的 KV 上限压到 1GiB
kvctl limit-percent kvc_m1 40  # 或按整卡百分比
kvctl list                     # 确认 total 生效(引擎内 resize_watcher 100ms 轮询自适应)
```

**T4:直接按协议读段**(这就是我们 controller/agent 的观测数据源,`MemInfoStruct` = 24 字节、3 个小端 int64):

```bash
python3 - <<'EOF'
import struct
for name in ("kvc_m1", "kvc_m2"):
    total, used, prealloc = struct.unpack("<3q", open(f"/dev/shm/{name}","rb").read(24))
    print(f"{name}: total={total/2**20:.0f}MiB used={used/2**20:.0f}MiB prealloc={prealloc/2**20:.0f}MiB")
EOF
```
期望:`total` 与 `kvctl limit` 设的值一致;打一轮请求后 `used` 上涨、请求结束后回落。

---

## 5. T5/T6 — vLLM Sleep(权重卸载)

```bash
# 记 sleep 前显存 → 睡 B → 观察下降(上次 0.6B/0.5B 级别掉 ~1.5GiB)
curl -X POST 'localhost:20001/sleep?level=1'
curl -s 'localhost:20001/is_sleeping'          # → true
sleep 8   # 等 unmap 反映到 nvidia-smi

# 唤醒并计时(level 1 = 从 pinned DRAM 回拷,快)
time curl -X POST 'localhost:20001/wake_up'
curl -s localhost:20001/v1/chat/completions -H 'Content-Type: application/json' \
  -d '{"model":"qwen2.5-0.5b","messages":[{"role":"user","content":"back?"}],"max_tokens":8}'
```

**T6 level 对比**:
```bash
curl -X POST 'localhost:20001/sleep?level=2'   # 权重+KV 全丢弃,无 DRAM 备份
time curl -X POST 'localhost:20001/wake_up'    # 需从磁盘重载权重 → 明显更慢
```
| | level 1 | level 2 |
|---|---|---|
| 权重去向 | pinned CPU DRAM | 丢弃 |
| 显存释放 | ~权重大小(KV 丢弃) | 权重+KV 全部 |
| wake 代价 | DRAM 回拷(亚秒~秒) | 磁盘/缓存重载(秒~十秒) |
| 额外要求 | 宿主机 DRAM 够大(A10 box 222GB 无压力) | 无 |

两档都保留虚拟地址预留,指针跨 sleep/wake 不变(这也是它与"直接 kill 进程"的本质区别)。部分唤醒:`POST /wake_up?tags=weights`(先权重后 KV,RLHF 场景常用)。

---

## 6. T7 — 协同:evict 一个,另一个吃到 headroom

```bash
# B 睡下后,给 A 持续打长上下文请求,观察 A 的 KV 段 used 能涨过原先两模型均分时的水位
curl -X POST 'localhost:20001/sleep?level=1'
for i in $(seq 1 8); do
  curl -s localhost:20000/v1/chat/completions -H 'Content-Type: application/json' \
    -d '{"model":"qwen3-0.6b","messages":[{"role":"user","content":"write a 300 word story"}],"max_tokens":300}' >/dev/null &
done; wait
kvctl list    # A 的 used 上涨——B 让出的物理页被 A 的弹性 KV 复用
```
这一步验证的就是我们整个设计的物理基础:**kvcached 的按需映射让"睡掉的模型"腾出的 HBM 无需任何显式交接即可被同卡邻居使用**。

---

## 7. 踩坑清单(全部真机踩过)

1. **镜像 autopatch 会污染所有 python 进程**:`kvcached-vllm` 镜像对每次 python 启动都注入 kvcached(连带 import vllm/torch,~GB 级内存)。凡在该镜像里跑**非引擎**的辅助脚本/守护进程,必须 `ENABLE_KVCACHED=false KVCACHED_AUTOPATCH=0`,否则 OOM(症状:exit 137、日志为空)。
2. **IPC 名唯一 + 会被规范化**:kvcached 会把名字里的点等字符替换(`qwen2.5`→`qwen2-5`),用纯 `[a-z0-9_]` 名字最省事;两模型共用一个名字 = 预算段互踩,现象诡异。
3. `/sleep` 404 → 少了 `VLLM_SERVER_DEV_MODE=1`;sleep 报不支持 → 少了 `--enable-sleep-mode`。两个都要。
4. sleep 后 `nvidia-smi` 不会瞬间掉——unmap 有延迟,等几秒再读。
5. wake 后**第一条**请求可能带重建开销,测时延要看第二条。
6. level 1 依赖宿主机 DRAM 放权重,大模型注意 `free -g`。
7. `--gpu-memory-utilization` 与 kvcached 互斥,别带。
8. 下载慢/被限流:Qwen 系列无需 token;私有模型 `export HF_TOKEN=...`。

## 8. 收尾(成本纪律,必做)

```bash
# 本地执行:销毁实例 → 确认归零 → 删临时 key → 清本地私钥
curl -su "$LAMBDA_API_KEY:" https://cloud.lambdalabs.com/api/v1/instance-operations/terminate \
  -H 'Content-Type: application/json' -d '{"instance_ids":["<INSTANCE_ID>"]}'
# 轮询直到 data 为空数组(terminating → 消失,~1-2 分钟)
curl -su "$LAMBDA_API_KEY:" https://cloud.lambdalabs.com/api/v1/instances
# 删 ssh key(先 GET /ssh-keys 拿 id)
curl -su "$LAMBDA_API_KEY:" -X DELETE https://cloud.lambdalabs.com/api/v1/ssh-keys/<KEY_ID>
rm -f /tmp/lambda_test /tmp/lambda_test.pub
```

## 9. 结果记录模板

| 测试 | 通过 | 关键数字 | 备注 |
|---|---|---|---|
| T1 单引擎基线 | ☐ | 就绪耗时 __s;基线显存 __MiB | |
| T2 双模型共卡 | ☐ | 合计显存 __MiB;2/2 出词 | |
| T3 kvctl 预算 | ☐ | limit 生效延迟 __ | |
| T4 /dev/shm 直读 | ☐ | total/used/prealloc = | |
| T5 sleep L1 | ☐ | 释放 __MiB;wake __s | |
| T6 L1 vs L2 | ☐ | wake L1 __s / L2 __s | |
| T7 headroom 复用 | ☐ | A 的 KV used 峰值 __ | |
