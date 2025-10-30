#!/bin/bash
set -e

echo "🚀 Building MQTT-marine Docker Images..."

# Broker
echo "📦 Building mqtt-eos-broker..."
docker build --platform=linux/amd64 -t mqtt-marine-broker -f mqtt_marine/broker/Dockerfile.broker .

# Subscriber Client
echo "📦 Building mqtt-eos-sub..."
docker build  --platform=linux/amd64 -t mqtt-marine-sub -f mqtt_marine/sub_only_client/Dockerfile.client .

# Publisher Client
echo "📦 Building mqtt-eos-pub..."
docker build  --platform=linux/amd64 -t mqtt-marine-pub -f mqtt_marine/pub_only_client/Dockerfile.client .

# Satelite Client
echo "📦 Building mqtt-eos-satelite..."
docker build --platform=linux/amd64 -t mqtt-marine-satelite -f mqtt_marine/satelite/Dockerfile.satelite .

echo "✅ All images built successfully!"
