package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	MQTT "github.com/eclipse/paho.mqtt.golang"
)

func getenvDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

const maxRetry = 3

var lostChan = make(chan struct{})

func connectToBroker(broker, clientID string) (MQTT.Client, error) {
	opts := MQTT.NewClientOptions().AddBroker(broker)
	opts.SetClientID(clientID)
	opts.SetKeepAlive(5 * time.Second)
	opts.SetPingTimeout(3 * time.Second)
	opts.SetConnectTimeout(10 * time.Second)
	opts.SetCleanSession(true)

	opts.OnConnect = func(c MQTT.Client) {
		// keep quiet to ensure only two-line outputs per message
	}
	opts.OnConnectionLost = func(c MQTT.Client, err error) {
		select {
		case lostChan <- struct{}{}:
		default:
		}
	}

	client := MQTT.NewClient(opts)
	token := client.Connect()
	ok := token.Wait() && token.Error() == nil
	if !ok {
		return nil, token.Error()
	}
	return client, nil
}

func connectAndSubscribeSingle(broker, clientID, subTopic string, handler MQTT.MessageHandler) (MQTT.Client, error) {
	for {
		var client MQTT.Client
		var err error
		for retry := 0; retry < maxRetry; retry++ {
			client, err = connectToBroker(broker, clientID)
			if err == nil {
				break
			}
			time.Sleep(2 * time.Second)
		}
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}

		var subErr error
		for retry := 0; retry < maxRetry; retry++ {
			token := client.Subscribe(subTopic, 0, handler)
			if token.Wait() && token.Error() != nil {
				subErr = token.Error()
				time.Sleep(2 * time.Second)
				continue
			}
			subErr = nil
			break
		}
		if subErr == nil {
			return client, nil
		}
		client.Disconnect(250)
		time.Sleep(3 * time.Second)
	}
}

func startReconnectLoopSingle(broker, clientID, subTopic string, handler MQTT.MessageHandler, client *MQTT.Client) {
	go func() {
		for range lostChan {
			(*client).Disconnect(250)
			for {
				newClient, err := connectAndSubscribeSingle(broker, clientID, subTopic, handler)
				if err != nil {
					time.Sleep(5 * time.Second)
					continue
				}
				*client = newClient
				break
			}
		}
	}()
}

func main() {
	subTopic := "buoy_sensors_data_prediction"
	saveDir := "/root/bin/msg_box"
	_ = os.MkdirAll(saveDir, 0755)

	var clientID string
	var brokerFlag string
	flag.StringVar(&clientID, "client_id", "marine_subscriber", "MQTT client id (must be unique per client)")
	flag.StringVar(&brokerFlag, "broker", "", "Single broker URL (e.g. tcp://127.0.0.1:1883)")
	flag.Parse()

	broker := strings.TrimSpace(brokerFlag)
	if broker == "" {
		broker = getenvDefault("BROKER", "tcp://127.0.0.1:1883")
	}

	handler := func(client MQTT.Client, msg MQTT.Message) {
		// Parse incoming CSV (header + one data line)
		lines := strings.Split(strings.TrimSpace(string(msg.Payload())), "\n")
		if len(lines) < 2 {
			return
		}
		header := lines[0]
		data := lines[1]

		headerFields := strings.Split(header, ",")
		dataFields := strings.Split(data, ",")

		// required columns
		stationIdx, sendTimeIdx, endToEndIdx := -1, -1, -1
		for i, h := range headerFields {
			switch h {
			case "Buoy-station":
				stationIdx = i
			case "send_time":
				sendTimeIdx = i
			case "End-to-End-LATENCY":
				endToEndIdx = i
			}
		}
		if stationIdx == -1 || sendTimeIdx == -1 {
			return
		}

		// compute end-to-end latency (ms)
		var sendTime float64
		fmt.Sscanf(dataFields[sendTimeIdx], "%f", &sendTime)
		recvTimeMs := float64(time.Now().UnixNano()) / 1e6
		latencyEndToEnd := int64(recvTimeMs - sendTime*1000)

		// ensure End-to-End-LATENCY exists and is updated
		if endToEndIdx == -1 {
			headerFields = append(headerFields, "End-to-End-LATENCY")
			dataFields = append(dataFields, fmt.Sprintf("%d", latencyEndToEnd))
		} else {
			dataFields[endToEndIdx] = fmt.Sprintf("%d", latencyEndToEnd)
		}

		// save to csv (append-only)
		stationID := dataFields[stationIdx]
		filename := fmt.Sprintf("%s/%s/%s.csv", saveDir, subTopic, stationID)
		dir := filepath.Dir(filename)
		if err := os.MkdirAll(dir, 0755); err == nil {
			writeHeader := false
			if _, err := os.Stat(filename); os.IsNotExist(err) {
				writeHeader = true
			}
			if f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
				if writeHeader {
					_, _ = f.WriteString(strings.Join(headerFields, ",") + "\n")
				}
				_, _ = f.WriteString(strings.Join(dataFields, ",") + "\n")
				_ = f.Close()
			}
		}

		// -------- ONLY TWO LINES TO STDOUT --------
		fmt.Println(strings.Join(headerFields, ","))
		fmt.Println(strings.Join(dataFields, ","))
	}

	var client MQTT.Client
	c, err := connectAndSubscribeSingle(broker, clientID, subTopic, handler)
	if err != nil {
		return
	}
	client = c
	startReconnectLoopSingle(broker, clientID, subTopic, handler, &client)

	// graceful exit
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	client.Disconnect(250)
}
