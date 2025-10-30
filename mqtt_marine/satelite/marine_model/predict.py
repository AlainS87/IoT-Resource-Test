import os
os.environ['TF_CPP_MIN_LOG_LEVEL'] = '3'   # 屏蔽 TF/keras 的底层日志

import warnings
warnings.filterwarnings("ignore")          # 全局屏蔽所有 python warnings

import pandas as pd
import numpy as np
import tensorflow as tf
import joblib
import sys
from datetime import datetime, timedelta

def main():
    # 屏蔽所有 stderr（包括底层 absl/TF警告）
    sys.stderr = open(os.devnull, 'w')

    if len(sys.argv) != 2:
        # 这里只是命令行参数用法提示，走stderr，实际上不会输出到Go端
        print("Usage: python predict.py <data.csv>", file=sys.stderr)
        sys.exit(1)
    csv_path = sys.argv[1]

    try:
        model = tf.keras.models.load_model('/root/app/marine_model/lstm_marine_model.h5', compile=False)
        model.compile(optimizer='adam', loss='mae', metrics=['mape'])
        scaler = joblib.load('/root/app/marine_model/marine_scaler.save')
    except Exception as e:
        print(f"Failed to load model or scaler: {e}", file=sys.stderr)
        sys.exit(1)

    features = [
        "YY", "MM", "DD", "hh", "mm", "WDIR", "WSPD", "GST", "WVHT",
        "DPD", "APD", "MWD", "PRES", "ATMP", "WTMP", "DEWP", "VIS", "TIDE", "station"
    ]

    try:
        df = pd.read_csv(csv_path).dropna().reset_index(drop=True)
        if len(df) < 24:
            print("data.csv must contain at least 24 rows", file=sys.stderr)
            sys.exit(1)
        last24 = df[features].iloc[-24:]
    except Exception as e:
        print(f"Failed to read/process input: {e}", file=sys.stderr)
        sys.exit(1)

    try:
        last24_scaled = scaler.transform(last24.values)
        X_input = last24_scaled.reshape(1, 24, len(features))
        y_pred = model.predict(X_input, verbose=0)[0]   # 不显示进度条
        dummy = np.zeros((1, len(features)))
        dummy[0, 8] = y_pred[0]   # WVHT
        dummy[0, 6] = y_pred[1]   # WSPD
        dummy[0, 9] = y_pred[2]   # DPD
        restored = scaler.inverse_transform(dummy)
        wvht_pred = restored[0, 8]
        wspd_pred = restored[0, 6]
        dpd_pred  = restored[0, 9]
    except Exception as e:
        print(f"Prediction failed: {e}", file=sys.stderr)
        sys.exit(1)

    try:
        row = df.iloc[-1]
        last_time = datetime(int(row["YY"]), int(row["MM"]), int(row["DD"]),
                             int(row["hh"]), int(row["mm"]))
        next_time = last_time + timedelta(minutes=30)
    except Exception as e:
        print(f"Time parsing failed: {e}", file=sys.stderr)
        sys.exit(1)

    wspd_kt = wspd_pred * 1.94384
    wvht_ft = wvht_pred * 3.28084

    # Wave hazard level based on table
    if wvht_ft <= 5:
        wave_alert = "Waves normal (<5 ft)"
    elif 6 <= wvht_ft <= 8:
        if dpd_pred <= 9:
            wave_alert = "Small craft caution (6-8 ft, period ≤9s)"
        else:
            wave_alert = "Waves normal (6-8 ft, period >9s)"
    elif 9 <= wvht_ft <= 11:
        if dpd_pred <= 9:
            wave_alert = "Hazardous seas (9-11 ft, period ≤9s)"
        elif 10 <= dpd_pred <= 12:
            wave_alert = "Small craft caution (9-11 ft, period 10-12s)"
        else:
            wave_alert = "Waves normal (9-11 ft, period ≥13s)"
    elif 12 <= wvht_ft <= 14:
        if dpd_pred <= 11:
            wave_alert = "Hazardous seas (12-14 ft, period ≤11s)"
        elif dpd_pred == 12:
            wave_alert = "Small craft caution (12-14 ft, period =12s)"
        else:
            wave_alert = "Waves normal (12-14 ft, period ≥13s)"
    elif wvht_ft >= 15:
        if dpd_pred <= 12:
            wave_alert = "Hazardous seas (>=15 ft, period ≤12s)"
        else:
            wave_alert = "Small craft caution (>=15 ft, period ≥13s)"
    else:
        wave_alert = "Unknown"

    # Wind warning
    if wspd_kt >= 64:
        wind_alert = "Hurricane warning (>=64 kt)"
    elif wspd_kt >= 48:
        wind_alert = "Storm warning (48-63 kt)"
    elif wspd_kt >= 34:
        wind_alert = "Gale warning (34-47 kt)"
    elif wspd_kt >= 22:
        wind_alert = "Small Craft Advisory (22-33 kt)"
    else:
        wind_alert = "Wind normal (<22 kt)"

    # Advice
    if ("Hazardous" in wave_alert) or (wind_alert.startswith("Hurricane")) or (wind_alert.startswith("Storm")) or (wind_alert.startswith("Gale")):
        advice = "⚠️ DANGER: Immediate evacuation or seek safe harbor"
    elif ("Small craft caution" in wave_alert) or (wind_alert.startswith("Small Craft")):
        advice = "⚠️ CAUTION: Exercise caution"
    else:
        advice = "✅ SAFE"


    # station 字段转成字符串、去掉小数点
    station_str = str(int(float(row['station'])))

    # 输出结构化CSV（仅两行！）
    print("time,wvht(m),wvht(ft),wave_level,wspd(m/s),wspd(kt),wind_level,dpd(s),advice,station")
    print(f"{next_time:%Y-%m-%d %H:%M},{wvht_pred:.3f},{wvht_ft:.2f},{wave_alert},{wspd_pred:.3f},{wspd_kt:.2f},{wind_alert},{dpd_pred:.3f},{advice},{station_str}")

if __name__ == "__main__":
    main()
