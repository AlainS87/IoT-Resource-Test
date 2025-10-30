import sys
import numpy as np
from tensorflow import keras
from scipy.special import softmax
import os
os.environ['TF_CPP_MIN_LOG_LEVEL'] = '3'  # 禁用所有 TensorFlow 日志
import tensorflow as tf
tf.get_logger().setLevel('ERROR')  # 只显示错误

def main():
    if len(sys.argv) < 2:
        print("Usage: python predict.py <npz_path>")
        sys.exit(1)

    npz_path = sys.argv[1]

    # 加载 npz 文件
    data = np.load(npz_path)
    # 自动识别 key
    if "zdisp" in data:
        zdisp = data["zdisp"]
    elif "zdisp_norw" in data:
        zdisp = data["zdisp_norw"]
    else:
        zdisp = data[list(data.keys())[0]]

    # 归一化
    significant_wave_height = 4 * np.std(zdisp)
    zdisp_norm = zdisp / significant_wave_height
    zdisp_norm = zdisp_norm.reshape(1, 1536, 1)

    file_str = "/root/app/rouge_wave_model/RWs_H_g_2p2_tadv_1min"
    LSTM_save_name = "best_LSTM_" + "RWs_H_g_2p2_tadv_1min" + ".h5"
    model = keras.models.load_model(file_str + '/' + LSTM_save_name)

    pred = model.predict(zdisp_norm, verbose=0)
    prob = softmax(pred, axis=-1)  # 多加一步保险

    norw_prob = prob[0, 0]
    rw_prob = prob[0, 1]
    wave_type_idx = int(np.argmax(prob, axis=-1)[0])
    wave_type_str = "non-rogue wave" if wave_type_idx == 0 else "rogue wave"

    print("norw_prob,rw_prob,wave_type_prediction")
    print(f"{norw_prob:.6f},{rw_prob:.6f},{wave_type_str}")

if __name__ == "__main__":
    main()
