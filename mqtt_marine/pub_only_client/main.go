package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	MQTT "github.com/eclipse/paho.mqtt.golang"
)

const maxRetry = 3

type BuoyFileState struct {
	Files []string
}

func getenvDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// Connect to a single broker with reasonable MQTT options and clear logs.
func connectToBroker(broker, clientID string) (MQTT.Client, error) {
	opts := MQTT.NewClientOptions().AddBroker(broker)
	opts.SetClientID(clientID)
	opts.SetKeepAlive(10 * time.Second)
	opts.SetPingTimeout(5 * time.Second)
	opts.SetConnectTimeout(10 * time.Second)
	opts.SetCleanSession(true)

	opts.OnConnect = func(c MQTT.Client) {
		fmt.Printf("[MQTT] Connected to %s as %s\n", broker, clientID)
	}
	opts.OnConnectionLost = func(c MQTT.Client, err error) {
		fmt.Printf("[MQTT] Connection lost from %s: %v\n", broker, err)
	}

	client := MQTT.NewClient(opts)
	fmt.Printf("[MQTT] Dialing %s ...\n", broker)
	token := client.Connect()
	ok := token.Wait() && token.Error() == nil
	if !ok {
		return nil, token.Error()
	}
	return client, nil
}

// Single-broker reconnect + publish loop.
// It never rotates broker; it will keep retrying the same broker indefinitely.
func sendWithReconnect(broker string, clientID, topic string, payload []byte) (MQTT.Client, error) {
	for {
		// connect with limited retries per cycle
		var client MQTT.Client
		var err error
		for retry := 0; retry < maxRetry; retry++ {
			client, err = connectToBroker(broker, clientID)
			if err == nil {
				break
			}
			fmt.Printf("[MQTT] Connect to %s failed (attempt %d/%d): %v\n", broker, retry+1, maxRetry, err)
			time.Sleep(2 * time.Second)
		}
		if err != nil {
			// after maxRetry failures, back off and try again (same broker)
			fmt.Printf("[MQTT] Failed to connect to %s after %d retries, will retry the same broker...\n", broker, maxRetry)
			time.Sleep(5 * time.Second)
			continue
		}

		// publish with retries on the same connection
		var pubErr error
		for retry := 0; retry < maxRetry; retry++ {
			token := client.Publish(topic, 0, false, payload)
			if token.Wait() && token.Error() != nil {
				fmt.Printf("[MQTT] Publish to %s failed (attempt %d/%d): %v\n", broker, retry+1, maxRetry, token.Error())
				pubErr = token.Error()
				time.Sleep(2 * time.Second)
				continue
			}
			pubErr = nil
			break
		}

		if pubErr == nil {
			return client, nil
		}

		// publish still failed after retries -> disconnect and retry the same broker
		fmt.Printf("[MQTT] Publish to %s failed after %d retries, will reconnect same broker...\n", broker, maxRetry)
		client.Disconnect(250)
		time.Sleep(3 * time.Second)
	}
}

func buoyWorker(buoy string, files []string, clientID, topic string, intervalSec int, broker string, wg *sync.WaitGroup) {
	defer wg.Done()
	idx := 0
	for {
		filePath := files[idx]
		fileData, err := ioutil.ReadFile(filePath)
		if err != nil {
			fmt.Printf("[%s] Read npz %s failed: %v\n", buoy, filePath, err)
			time.Sleep(time.Duration(intervalSec) * time.Second)
			continue
		}

		payloadStruct := map[string]interface{}{
			"buoy_id":   buoy,
			"filename":  filepath.Base(filePath),
			"data":      base64.StdEncoding.EncodeToString(fileData),
			"send_time": float64(time.Now().UnixNano()) / 1e9,
		}
		payloadBytes, err := json.Marshal(payloadStruct)
		if err != nil {
			fmt.Printf("[%s] JSON marshal failed: %v\n", buoy, err)
			time.Sleep(time.Duration(intervalSec) * time.Second)
			continue
		}

		client, err := sendWithReconnect(broker, clientID+"_"+buoy, topic, payloadBytes)
		if err != nil {
			// In current design, sendWithReconnect never returns error (it loops forever).
			// But keep this log just in case we change behavior in future.
			fmt.Printf("[%s] Broker unavailable, message failed: %v\n", buoy, err)
			time.Sleep(3 * time.Second)
			continue
		}
		fmt.Printf("[%s] Sent %s\n", buoy, filePath)
		client.Disconnect(250)

		idx = (idx + 1) % len(files) // next file
		time.Sleep(time.Duration(intervalSec) * time.Second)
	}
}

func main() {
	var (
		clientID   string
		baseFolder string
		sleepSec   int
		brokerFlag string
	)
	flag.StringVar(&clientID, "client_id", "EOS_publisher", "MQTT client id (base, will add _buoy)")
	flag.StringVar(&baseFolder, "base_folder", "/root/app/sample_msg", "Base folder containing buoy folders")
	flag.IntVar(&sleepSec, "interval", 1, "Sleep seconds for each buoy thread")
	flag.StringVar(&brokerFlag, "broker", "", "Single broker URL (e.g. tcp://127.0.0.1:1883)")
	flag.Parse()

	// Determine single broker: flag > env(BROKER) > default
	broker := strings.TrimSpace(brokerFlag)
	if broker == "" {
		broker = getenvDefault("BROKER", "tcp://127.0.0.1:1883")
	}
	fmt.Printf("[Startup] Broker: %s\n", broker)

	topic := "buoy_sensors_data"
	buoyDirs, err := os.ReadDir(baseFolder)
	if err != nil {
		fmt.Println("Failed to read sample_msg dir:", err)
		return
	}

	var wg sync.WaitGroup
	buoyCnt := 0
	for _, d := range buoyDirs {
		if d.IsDir() {
			dirPath := filepath.Join(baseFolder, d.Name())
			files, err := os.ReadDir(dirPath)
			if err != nil {
				fmt.Printf("Read dir %s failed: %v\n", dirPath, err)
				continue
			}
			var npzList []string
			for _, f := range files {
				if !f.IsDir() && filepath.Ext(f.Name()) == ".npz" {
					npzList = append(npzList, f.Name())
				}
			}
			sort.Strings(npzList)
			fullPaths := make([]string, len(npzList))
			for i, fn := range npzList {
				fullPaths[i] = filepath.Join(dirPath, fn)
			}
			if len(fullPaths) > 0 {
				wg.Add(1)
				go buoyWorker(d.Name(), fullPaths, clientID, topic, sleepSec, broker, &wg)
				buoyCnt++
			}
		}
	}

	if buoyCnt == 0 {
		fmt.Println("No buoy folders with npz files found, exit.")
		return
	}
	wg.Wait()
}
