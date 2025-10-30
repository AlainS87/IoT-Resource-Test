#!/usr/bin/env bash
set -euo pipefail

# =======================
# Configurable parameters
# =======================
SATELLITE_IMG="mqtt-marine-satelite"   # 你现有的 broker/satellite 镜像名
PUBLISHER_IMG="mqtt-marine-pub"        # publisher 镜像
SUBSCRIBER_IMG="mqtt-marine-sub"       # subscriber 镜像

NET_NAME="marine-net"
BROKER_NAME="satellite"
PUB_NAME="publisher"
SUB_NAME="subscriber"

BROKER_PORT=1883
PUB_INTERVAL=1
PUB_CLIENT_ID="marine_publisher"
SUB_CLIENT_ID="marine_subscriber"

SUB_TOPIC="buoy_sensors_data"
PUB_TOPIC="buoy_sensors_data_prediction"

# Sweep sets
RTT_SET=(20 40 80 160)                 # ms
CPU_SET=(1 2 3 4)                      # cores
MEM_SET=(128 256 512 1024 2048)        # MB

# Per-experiment timing
WARMUP_SEC=60                          # 等待 1 分钟进入稳定期
MEASURE_SEC=180                        # 在 [1, 4] 分钟窗口测量 180 秒
TEARDOWN_GRACE=5                       # 容器停止的缓冲时间

# Output
RESULT_CSV="qos_results.csv"
LOG_DIR="./_logs_marine"
mkdir -p "$LOG_DIR"

# =======================
# Helpers
# =======================
log() { echo "[sweep] $*"; }

cleanup_run() {
  # 停止（如果在跑）并移除容器 (都用了 --rm，stop 就会移除)
  docker stop "$PUB_NAME" >/dev/null 2>&1 || true
  docker stop "$SUB_NAME" >/dev/null 2>&1 || true
  docker stop "$BROKER_NAME" >/dev/null 2>&1 || true
}

ensure_network() {
  docker network create "$NET_NAME" >/dev/null 2>&1 || true
}

start_broker() {
  log "Starting satellite broker ..."
  docker run --rm -d --name "$BROKER_NAME" \
    --network "$NET_NAME" \
    -p ${BROKER_PORT}:1883 \
    -e SUB_TOPIC="$SUB_TOPIC" \
    -e PUB_TOPIC="$PUB_TOPIC" \
    "$SATELLITE_IMG" >/dev/null

  # 简单等待
  sleep 3
}

start_publisher() {
  log "Starting publisher ..."
  docker run --rm -d --name "$PUB_NAME" \
    --network "$NET_NAME" \
    -e BROKER="tcp://${BROKER_NAME}:1883" \
    -e INTERVAL="${PUB_INTERVAL}" \
    -e CLIENT_ID="${PUB_CLIENT_ID}" \
    "$PUBLISHER_IMG" >/dev/null
}

start_subscriber() {
  local cpu="$1" mem_mb="$2"
  log "Starting subscriber (cpus=${cpu}, mem=${mem_mb}MB) ..."
  docker run --rm -d --name "$SUB_NAME" \
    --network "$NET_NAME" \
    --cpus="${cpu}" \
    --memory="${mem_mb}m" \
    --memory-swap="${mem_mb}m" \
    -e BROKER="tcp://${BROKER_NAME}:1883" \
    -e CLIENT_ID="${SUB_CLIENT_ID}" \
    "$SUBSCRIBER_IMG" >/dev/null
}

# 抓取 subscriber 在窗口期的日志到文件
collect_window_logs() {
  local out_file="$1"

  # 进入稳定期
  sleep "$WARMUP_SEC"

  # 从“现在”起开始抓取 180 秒日志
  # 注意：-f 会持续跟随，我们跑在后台，并在到时后 kill 它
  : > "$out_file"
  # -t 为日志加时间戳，虽然这份方案不依赖解析时间戳，但留着更好排障
  docker logs -f --since=0s -t "$SUB_NAME" > "$out_file" 2>&1 &
  local tail_pid=$!

  sleep "$MEASURE_SEC"
  kill "$tail_pid" >/dev/null 2>&1 || true
  wait "$tail_pid" 2>/dev/null || true
}

# 从日志中抽取 End-to-End-LATENCY 并取均值
# 日志的每条“打印”会输出两行：第一行为 header，第二行为 data
# 我们用 awk 记录最近一次 header 的列索引；遇到下一行 data 就抓数值。
# 输出：均值 和 样本数
compute_latency_avg() {
  local log_file="$1"

  awk -F',' '
    function trim(s){gsub(/^[ \t]+|[ \t]+$/, "", s);return s}
    BEGIN{
      have_header=0; target_idx=-1; sum=0; cnt=0;
    }
    {
      # 去掉 docker 日志前缀的时间戳部分 —— 以第一个空格后为内容
      line=$0
      # 如果你的 docker 版本前缀带时间戳，这里尝试截掉；若没有也不影响
      split(line, parts, "] ")
      if (index(line,"] ")>0) { line=parts[length(parts)] }

      # 不是 CSV 就跳过
      if (index(line, ",") == 0) next

      # 判断是否 header：包含 End-to-End-LATENCY 字段
      if (index(line, "End-to-End-LATENCY")>0) {
        # 解析 header 找到指定列
        n=split(line, H, ",")
        target_idx=-1
        for (i=1;i<=n;i++){
          h=trim(H[i])
          if (h=="End-to-End-LATENCY") { target_idx=i; break }
        }
        have_header=1
        next
      }

      # 如果上一行是 header，则这行应是 data
      if (have_header==1 && target_idx>0) {
        m=split(line, D, ",")
        if (target_idx<=m) {
          val=trim(D[target_idx])
          # 只接受整数或纯数字
          if (val ~ /^[0-9]+$/) {
            sum+=val+0
            cnt+=1
          }
        }
        have_header=0
      }
    }
    END{
      if (cnt>0) {
        avg = sum / cnt
        printf("%.6f,%d\n", avg, cnt)
      } else {
        printf("NaN,0\n")
      }
    }
  ' "$log_file"
}

# =======================
# Main sweep
# =======================
echo "rtt_ms,cpu_cores,mem_mb,avg_qos_ms,samples" > "$RESULT_CSV"

ensure_network

for rtt in "${RTT_SET[@]}"; do
  for cpu in "${CPU_SET[@]}"; do
    for mem in "${MEM_SET[@]}"; do
      log "======== Experiment: RTT=${rtt}ms, CPU=${cpu}, MEM=${mem}MB ========"

      # 保险起见，清理旧容器
      cleanup_run

      # 启动三者
      start_broker
      start_subscriber "$cpu" "$mem"
      start_publisher

      # 收集窗口日志
      RUN_LOG="${LOG_DIR}/sub_${rtt}ms_${cpu}cpu_${mem}mb.log"
      collect_window_logs "$RUN_LOG"

      # 计算纯 End-to-End-LATENCY 均值 & 样本数
      read -r avg_raw cnt <<<"$(compute_latency_avg "$RUN_LOG" | tr ',' ' ')"

      # QoS = E2E + RTT（按你的需求“加上 rtt 来模拟 delay”）
      if [[ "$avg_raw" == "NaN" || "$cnt" -eq 0 ]]; then
        avg_qos="NaN"
      else
        # bash 浮点：用 awk 做简单加法
        avg_qos="$(awk -v a="$avg_raw" -v b="$rtt" 'BEGIN{printf "%.6f", a+b}')"
      fi

      echo "${rtt},${cpu},${mem},${avg_qos},${cnt}" | tee -a "$RESULT_CSV" >/dev/null

      # 停止容器
      cleanup_run
      sleep "$TEARDOWN_GRACE"
    done
  done
done

log "All experiments done. Results saved to ${RESULT_CSV}"
