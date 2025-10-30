#!/bin/bash

set -e

echo "📥 Downloading benchmark test data..."

# Ensure gdown is installed
if ! command -v gdown &> /dev/null; then
    echo "⚠️  'gdown' is not installed. Installing with pip..."
    pip install gdown
fi

# Ensure unzip is installed
if ! command -v unzip &> /dev/null; then
    echo "⚠️  'unzip' is not installed. Installing with apt-get..."
    sudo apt-get update && sudo apt-get install -y unzip
fi

# Create target directories if they don't exist
mkdir -p ./mqtt_marine/pub_only_client/sample_msg
mkdir -p ./mqtt_marine/satelite/msg_box/buoy_sensors_data/46268
mkdir -p ./mqtt_marine/pub_only_client/sample_msg/wave_height_g_2p2

# Download MQTT-marine ML input CSVs
echo "⬇️  Downloading MQTT-marine sample message files..."
gdown --id 1swMBr2hkpeNe6Drkl0At-kPeDgMYY3np -O ./mqtt_marine/pub_only_client/sample_msg/sample_msg.csv
gdown --id 1pI2jelXPaYWUbWl57A_-n8vDwXRy21j1 -O ./mqtt_marine/satelite/msg_box/buoy_sensors_data/46268/46268.csv

# Download and extract buoy data for rogue wave prediction
echo "⬇️  Downloading buoy data for rogue wave prediction..."
BUOY_DIR=./mqtt_marine/pub_only_client/sample_msg/wave_height_g_2p2
BUOY_ZIP=$BUOY_DIR/buoy_data.zip
gdown --id 1F380_V2DY0pem_6mlDPbapAGLatZ7F9y -O "$BUOY_ZIP"

echo "🗜️  Extracting buoy data..."
unzip -o "$BUOY_ZIP" -d "$BUOY_DIR"
rm -f "$BUOY_ZIP"

echo "✅ All files downloaded and prepared successfully."
