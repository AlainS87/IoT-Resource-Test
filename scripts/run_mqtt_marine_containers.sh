#!/bin/bash
set -e

echo "🚀 Run MQTT-marine Docker Containers..."

# 创建一个自定义网络（如果已存在则忽略错误）
docker network create marine-net || true

# 启动 satellite（MQTT broker）
docker run --rm --name satellite \
  --network marine-net \
  -p 1883:1883 \
  -e SUB_TOPIC=buoy_sensors_data \
  -e PUB_TOPIC=buoy_sensors_data_prediction \
  mqtt-marine-satelite &

# 等待 broker 启动一会儿
sleep 3

# 启动 publisher，并使用容器名 satellite 作为 broker 地址
docker run --rm --name publisher \
  --network marine-net \
  -e BROKER="tcp://satellite:1883" \
  -e INTERVAL=1 \
  -e CLIENT_ID="marine_publisher" \
  mqtt-marine-pub

docker run --rm --name subscriber \
    --network marine-net \
  -e BROKER="tcp://satellite:1883" \
  -e CLIENT_ID="marine_subscriber" \
  mqtt-marine-sub
