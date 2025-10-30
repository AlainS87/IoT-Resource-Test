package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	MQTT "github.com/eclipse/paho.mqtt.golang"
)

// -------------------------------------------------------------------
// Config: local embedded broker
// -------------------------------------------------------------------
var brokerURL = getenvDefault("BROKER_URL", "tcp://127.0.0.1:1883") // satellite hosts mosquitto itself

const maxRetry = 3

var lostChan = make(chan struct{})
var msgChan = make(chan MQTT.Message, 128)
var globalClient MQTT.Client
var clientMutex sync.RWMutex
var workerDone = make(chan struct{})
var workerHeartbeat = make(chan struct{}, 1)

// Message de-dup
var processedMessages = make(map[string]time.Time)
var msgMutex sync.RWMutex
var messageID = 0
var msgIDMutex sync.Mutex

func getenvDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func generateMessageID() int {
	msgIDMutex.Lock()
	defer msgIDMutex.Unlock()
	messageID++
	return messageID
}

func cleanupOldMessages() {
	msgMutex.Lock()
	defer msgMutex.Unlock()
	cutoff := time.Now().Add(-5 * time.Minute)
	for key, ts := range processedMessages {
		if ts.Before(cutoff) {
			delete(processedMessages, key)
		}
	}
}

func isMessageProcessed(payload string) bool {
	msgMutex.RLock()
	defer msgMutex.RUnlock()
	_, exists := processedMessages[payload]
	return exists
}

func markMessageProcessed(payload string) {
	msgMutex.Lock()
	defer msgMutex.Unlock()
	processedMessages[payload] = time.Now()
}

// -------------------------------------------------------------------
// Connect to local broker and subscribe
// -------------------------------------------------------------------
func connectAndSubscribeLocal(clientID, subTopic string, handler MQTT.MessageHandler) (MQTT.Client, error) {
	for retry := 0; retry < maxRetry; retry++ {
		fmt.Printf("[MQTT] Connecting to %s (attempt %d/%d)\n", brokerURL, retry+1, maxRetry)

		uniqueClientID := fmt.Sprintf("%s_%d", clientID, time.Now().UnixNano())

		opts := MQTT.NewClientOptions().AddBroker(brokerURL)
		opts.SetClientID(uniqueClientID)
		opts.SetKeepAlive(5 * time.Second)
		opts.SetPingTimeout(3 * time.Second)
		opts.SetCleanSession(true)

		opts.OnConnectionLost = func(client MQTT.Client, err error) {
			fmt.Printf("[MQTT] Connection lost: %v\n", err)
			select {
			case lostChan <- struct{}{}:
			default:
			}
		}
		opts.OnReconnecting = func(MQTT.Client, *MQTT.ClientOptions) {
			fmt.Println("[MQTT] Reconnecting...")
		}
		opts.OnConnect = func(MQTT.Client) {
			fmt.Println("[MQTT] Connected (OnConnect)")
		}

		c := MQTT.NewClient(opts)
		token := c.Connect()
		if ok := token.Wait() && token.Error() == nil; ok {
			fmt.Printf("[MQTT] Subscribing to %s\n", subTopic)
			t2 := c.Subscribe(subTopic, 0, handler)
			if t2.Wait() && t2.Error() == nil {
				fmt.Printf("[MQTT] Connected & subscribed to %s via %s\n", subTopic, brokerURL)
				return c, nil
			}
			fmt.Printf("[MQTT] Subscribe failed: %v\n", t2.Error())
			c.Disconnect(250)
			break
		}
		fmt.Printf("[MQTT] Connect failed (attempt %d/%d): %v\n", retry+1, maxRetry, token.Error())
		time.Sleep(2 * time.Second)
	}
	return nil, fmt.Errorf("local broker unreachable at %s", brokerURL)
}

func startReconnectLoopLocal(clientID, subTopic string, handler MQTT.MessageHandler, client *MQTT.Client) {
	go func() {
		for range lostChan {
			fmt.Println("[MQTT] Lost connection. Attempting reconnect...")
			clientMutex.Lock()
			if *client != nil && (*client).IsConnected() {
				(*client).Disconnect(250)
			}
			clientMutex.Unlock()

			for {
				newClient, err := connectAndSubscribeLocal(clientID, subTopic, handler)
				if err != nil {
					fmt.Println("[MQTT] Reconnect failed; retry in 5s:", err)
					time.Sleep(5 * time.Second)
					continue
				}
				clientMutex.Lock()
				*client = newClient
				globalClient = newClient
				clientMutex.Unlock()
				fmt.Println("[MQTT] Reconnected successfully.")
				break
			}
		}
	}()
}

// -------------------------------------------------------------------
// Worker
// -------------------------------------------------------------------
func startWorker() {
	go func() {
		workerID := 0
		for {
			workerID++
			fmt.Printf("[Worker] Starting instance #%d\n", workerID)
			func() {
				defer func() {
					if r := recover(); r != nil {
						fmt.Printf("[Worker #%d] PANIC recovered: %v\n", workerID, r)
					}
					select {
					case workerDone <- struct{}{}:
					default:
					}
					fmt.Printf("[Worker #%d] exited, will restart...\n", workerID)
				}()

				for msg := range msgChan {
					select {
					case workerHeartbeat <- struct{}{}:
					default:
					}
					handlePrediction(msg)
				}
			}()
			time.Sleep(1 * time.Second)
		}
	}()
}

// -------------------------------------------------------------------
// Main
// -------------------------------------------------------------------
func main() {
	subTopic := getenvDefault("SUB_TOPIC", "buoy_sensors_data")
	pubTopic := getenvDefault("PUB_TOPIC", "buoy_sensors_data_prediction")
	saveDir := getenvDefault("SAVE_DIR", "/root/bin/msg_box")
	clientID := getenvDefault("CLIENT_ID", "marine_satelite")

	fmt.Printf("[Startup] ClientID=%s Broker=%s SUB=%s PUB=%s\n", clientID, brokerURL, subTopic, pubTopic)

	if err := os.MkdirAll(saveDir, 0755); err != nil {
		fmt.Println("[Startup] mkdir failed:", err)
		return
	}

	// periodic dedup cleanup
	go func() {
		tk := time.NewTicker(1 * time.Minute)
		defer tk.Stop()
		for range tk.C {
			cleanupOldMessages()
		}
	}()

	// simple watchdog
	go func() {
		lastExit := time.Now()
		rest := 0
		lastBeat := time.Now()
		for {
			select {
			case <-workerDone:
				rest++
				lastExit = time.Now()
			case <-workerHeartbeat:
				lastBeat = time.Now()
			case <-time.After(15 * time.Second):
				fmt.Printf("[Watchdog] alive=%s buf=%d cache=%d lastBeat=%s restarts=%d lastExit=%s\n",
					time.Now().Format(time.RFC3339Nano),
					len(msgChan), len(processedMessages),
					lastBeat.Format(time.RFC3339), rest, lastExit.Format(time.RFC3339))
			}
		}
	}()

	startWorker()

	// handler with dedup
	handler := func(_ MQTT.Client, msg MQTT.Message) {
		msgID := generateMessageID()
		payload := string(msg.Payload())
		fmt.Printf("[Handler #%d] msg on %s, size=%d bytes\n", msgID, msg.Topic(), len(payload))

		if isMessageProcessed(payload) {
			fmt.Printf("[Handler #%d] DUP detected, skipping\n", msgID)
			return
		}
		markMessageProcessed(payload)

		select {
		case msgChan <- msg:
			fmt.Printf("[Handler #%d] queued; buf=%d\n", msgID, len(msgChan))
		default:
			fmt.Printf("[Handler #%d] buffer full; dropping\n", msgID)
		}
	}

	// initial connect to local broker
	var client MQTT.Client
	c, err := connectAndSubscribeLocal(clientID, subTopic, handler)
	if err != nil {
		fmt.Println("[MQTT] Initial connect failed:", err)
		return
	}
	client = c
	clientMutex.Lock()
	globalClient = c
	clientMutex.Unlock()
	startReconnectLoopLocal(clientID, subTopic, handler, &client)

	// wait for signals
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	fmt.Println("Exiting satellite.")

	clientMutex.RLock()
	if globalClient != nil {
		globalClient.Disconnect(250)
	}
	clientMutex.RUnlock()
}

// -------------------------------------------------------------------
// ML prediction + publish
// -------------------------------------------------------------------
func handlePrediction(msg MQTT.Message) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[Worker] PANIC in handlePrediction: %v\n", r)
		}
	}()

	recvTime := time.Now().UnixNano() / 1e6
	type Payload struct {
		BuoyID   string  `json:"buoy_id"`
		Filename string  `json:"filename"`
		Data     string  `json:"data"`
		SendTime float64 `json:"send_time"`
	}
	var payload Payload
	if err := json.Unmarshal(msg.Payload(), &payload); err != nil {
		fmt.Printf("[Worker] JSON error: %v\n", err)
		return
	}

	npzBytes, err := base64.StdEncoding.DecodeString(payload.Data)
	if err != nil {
		fmt.Printf("[Worker] b64 error: %v\n", err)
		return
	}

	tmpDir := "/tmp/mqtt_npz"
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		fmt.Printf("[Worker] MkdirAll error: %v\n", err)
		return
	}
	tmpPath := fmt.Sprintf("%s/%s", tmpDir, payload.Filename)
	if err := os.WriteFile(tmpPath, npzBytes, 0644); err != nil {
		fmt.Printf("[Worker] WriteFile error: %v\n", err)
		return
	}

	latencyReception := int64(0)
	if payload.SendTime > 0 {
		latencyReception = recvTime - int64(payload.SendTime*1000)
	}

	pyResult, err := runPythonPredict(tmpPath)
	if err != nil {
		fmt.Printf("[Worker] ML prediction failed: %v\n", err)
		pyResult = "PredictionError"
	}
	nowMs := time.Now().UnixNano() / 1e6
	latencyInference := int64(0)
	if payload.SendTime > 0 {
		latencyInference = nowMs - int64(payload.SendTime*1000)
	}

	pyLines := strings.Split(strings.TrimSpace(pyResult), "\n")
	var csvLines []string
	for _, line := range pyLines {
		line = strings.TrimSpace(line)
		if line == "" ||
			strings.Contains(line, "tensorflow") ||
			strings.Contains(line, "cudart") ||
			strings.Contains(line, "dlerror") ||
			strings.Contains(line, "libcudart") ||
			strings.Contains(line, "GPU") ||
			strings.Contains(line, "CUDA") ||
			strings.Contains(line, "stream_executor") ||
			strings.Contains(line, "dso_loader") ||
			strings.Contains(line, "Could not load dynamic library") ||
			strings.Contains(line, "Ignore above") {
			continue
		}
		csvLines = append(csvLines, line)
	}
	if len(csvLines) < 2 {
		fmt.Printf("[Worker] No valid CSV lines in result\n")
		_ = os.Remove(tmpPath)
		return
	}
	header := csvLines[0]
	data := csvLines[1]
	finalHeader := "Buoy-station," + header + ",Observation-to-Reception-LATENCY,Observation-to-Inference-LATENCY,send_time"
	finalData := fmt.Sprintf("%s,%s,%d,%d,%.6f", payload.BuoyID, data, latencyReception, latencyInference, payload.SendTime)
	sendMsg := finalHeader + "\n" + finalData

	// publish result
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("[Publisher] PANIC: %v\n", r)
			}
		}()
		clientMutex.RLock()
		client := globalClient
		clientMutex.RUnlock()
		if client != nil && client.IsConnected() {
			done := make(chan bool, 1)
			go func() {
				token := client.Publish(getenvDefault("PUB_TOPIC", "buoy_sensors_data_prediction"), 0, false, sendMsg)
				_ = token.Wait()
				if token.Error() == nil {
					fmt.Println("[Worker] Published prediction result")
				} else {
					fmt.Printf("[Worker] Publish error: %v\n", token.Error())
				}
				done <- true
			}()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				fmt.Println("[Worker] Publish timeout (3s)")
			}
		} else {
			fmt.Println("[Worker] Client not connected; skip publish")
		}
	}()
	_ = os.Remove(tmpPath)
}

func runPythonPredict(npzPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python", "/root/app/rouge_wave_model/predict.py", npzPath)
	cmd.Env = append(os.Environ(),
		"TF_CPP_MIN_LOG_LEVEL=3",
		"TF_ENABLE_ONEDNN_OPTS=0",
	)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "PredictionTimeout", ctx.Err()
	}
	if err != nil {
		return "PredictionError", err
	}
	return string(out), nil
}
