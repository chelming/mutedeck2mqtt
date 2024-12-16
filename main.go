package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Constants
const (
	object_id = "mutedeck2mqtt_device"

	DEBUG = iota
	INFO
	WARN
	ERROR
)

// Global variable to store the current log level
var logLevel = INFO

// Map to store successfully sent discovery topics
var discoveryTopics = make(map[string]bool)
var mu sync.Mutex

// Custom logger function
func logMessage(level int, message string) {
	if level >= logLevel {
		var levelStr string
		switch level {
		case DEBUG:
			levelStr = "DEBUG"
		case INFO:
			levelStr = "INFO"
		case WARN:
			levelStr = "WARN"
		case ERROR:
			levelStr = "ERROR"
		}
		log.Printf("[%s] %s\n", levelStr, message)
	}
}

// Function to get the client's IP address
func getClientIP(r *http.Request) string {
	forwarded := r.Header.Get("X-FORWARDED-FOR")
	if forwarded != "" {
		// If there are multiple IPs, take the first one
		return strings.Split(forwarded, ",")[0]
	}
	return r.RemoteAddr
}

func getPlatformName(input string) string {
	switch {
	case strings.HasPrefix(input, "zoom"):
		return "Zoom"
	case strings.HasPrefix(input, "teams"):
		return "Teams"
	case input == "webex":
		return "Webex"
	case input == "streamyard":
		return "StreamYard"
	case input == "google-meet":
		return "Google Meet"
	default:
		return toTitleCase(input)
	}
}

func toTitleCase(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	caser := cases.Title(language.English)
	return caser.String(s)
}

// Single discovery payload
type Device struct {
	IDs             []string `json:"ids"`
	Name            string   `json:"name"`
	Manufacturer    string   `json:"mf"`
	Model           string   `json:"mdl"`
	SoftwareVersion string   `json:"sw"`
	SerialNumber    string   `json:"sn"`
	HardwareVersion string   `json:"hw"`
}

type Origin struct {
	Name            string `json:"name"`
	SoftwareVersion string `json:"sw"`
	URL             string `json:"url"`
}

type Component struct {
	CommandTopic     string   `json:"cmd_t"`
	EnabledByDefault bool     `json:"en"`
	EntityCategory   string   `json:"ent_cat"`
	Icon             string   `json:"icon"`
	Name             string   `json:"name"`
	ObjectID         string   `json:"obj_id"`
	Optimistic       bool     `json:"opt"`
	Options          []string `json:"options"`
	Platform         string   `json:"p"`
	StateTopic       string   `json:"stat_t"`
	UniqueID         string   `json:"uniq_id"`
	ValueTemplate    string   `json:"val_tpl"`
}

type DiscoveryPayloadStruct struct {
	Device           Device               `json:"dev"`
	Origin           Origin               `json:"o"`
	Components       map[string]Component `json:"cmps"`
	StateTopic       string               `json:"stat_t"`
	QualityOfService int                  `json:"qos"`
}

var discoveryMessages = make(map[string]DiscoveryPayloadStruct)

func main() {
	// Set log level from environment variable
	logLevelStr := os.Getenv("LOG_LEVEL")
	switch strings.ToUpper(logLevelStr) {
	case "DEBUG":
		logLevel = DEBUG
	case "INFO":
		logLevel = INFO
	case "WARN":
		logLevel = WARN
	case "ERROR":
		logLevel = ERROR
	default:
		logLevel = INFO
	}

	// Check for required environment variables
	var missingVars []string

	// Check for MQTT_HOST
	MQTT_HOST := os.Getenv("MQTT_HOST")
	if MQTT_HOST == "" {
		missingVars = append(missingVars, "MQTT_HOST")
	} else {
		logMessage(INFO, fmt.Sprintf("Using MQTT server: %s", MQTT_HOST))
	}

	// Check for MQTT_PASS
	MQTT_PASS := os.Getenv("MQTT_PASS")
	if MQTT_PASS == "" {
		missingVars = append(missingVars, "MQTT_PASS")
	}

	// Check for MQTT_USER
	MQTT_USER := os.Getenv("MQTT_USER")
	if MQTT_USER == "" {
		missingVars = append(missingVars, "MQTT_USER")
	}

	// Log fatal error if any variables are missing
	if len(missingVars) > 0 {
		log.Fatalf("Missing environment variables: %v", missingVars)
	}

	// Check for MQTT_PORT and default to 1883
	MQTT_PORT := 1883
	if portStr := os.Getenv("MQTT_PORT"); portStr != "" {
		port, err := strconv.Atoi(portStr)
		if err != nil {
			log.Fatalf("Invalid MQTT_PORT: %v", err)
		}
		MQTT_PORT = port
	}

	// Check for a discovery prefix
	discovery_prefix := os.Getenv("HOME_ASSISTANT_DISCOVERY_TOPIC")
	if discovery_prefix == "" {
		discovery_prefix = "homeassistant"
	}

	// Set client identifier
	clientID := os.Getenv("MQTT_CLIENT_ID")
	if clientID == "" {
		clientID = "mutedeck2mqtt"
	}

	// MQTT client options
	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://%s:%d", MQTT_HOST, MQTT_PORT))
	opts.SetClientID(clientID)
	opts.SetUsername(MQTT_USER)
	opts.SetPassword(MQTT_PASS)

	// Create and start the MQTT client
	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		log.Fatal(token.Error())
	}

	// Subscribe to homeassistant/status topic
	client.Subscribe("homeassistant/status", 0, func(client mqtt.Client, msg mqtt.Message) {
		if string(msg.Payload()) == "online" {
			logMessage(INFO, "Home Assistant is online, resending discovery message")
			resendDiscoveryMessages(client)
		}
	})

	// HTTP server handler
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Get the client's IP address
		clientIP := getClientIP(r)
		logMessage(DEBUG, fmt.Sprintf("Request received from IP: %s", clientIP))

		// Read the body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Print the incoming body
		logMessage(DEBUG, fmt.Sprintf("Incoming body: %s", string(body)))

		// Parse JSON body
		var data map[string]interface{}
		err = json.Unmarshal(body, &data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Validate JSON keys
		requiredKeys := []string{"call", "control", "mute", "record", "share", "video"}
		for _, key := range requiredKeys {
			if _, ok := data[key]; !ok {
				logMessage(ERROR, fmt.Sprintf("Request from %s missing required key: %s", clientIP, key))
				http.Error(w, fmt.Sprintf("Missing required key: %s", key), http.StatusBadRequest)
				return
			}
		}

		// Process the control field through getPlatformName
		if control, ok := data["control"].(string); ok {
			data["control"] = getPlatformName(control)
		}

		// Get MQTT topic and prefix from URL parameters
		topic := r.URL.Query().Get("topic")
		if topic == "" {
			topic = "mutedeck"
		}
		prefix := r.URL.Query().Get("prefix")
		if prefix == "" {
			prefix = "mutedeck2mqtt"
		}

		logMessage(DEBUG, "Checking discovery topic")

		discoveryTopic := fmt.Sprintf("%s/%s/%s_%s/config", discovery_prefix, "device", object_id, topic)
		mu.Lock()
		if !discoveryTopics[discoveryTopic] {
			logMessage(DEBUG, "Preparing discovery topic")
			// Create the discovery message
			discoveryPayload := DiscoveryPayloadStruct{
				Device: Device{
					IDs:          []string{fmt.Sprintf("%s_%s", object_id, topic)},
					Name:         toTitleCase(topic),
					Manufacturer: "MuteDeck",
				},
				Origin: Origin{
					Name:            "MuteDeck2MQTT",
					SoftwareVersion: "2024.12.16",
					URL:             "https://github.com/chelming/mutedeck2mqtt/",
				},
				Components: map[string]Component{
					fmt.Sprintf("%s_%s", topic, "call"): {
						CommandTopic:     "mutedeck2mqtt/no-reply",
						EnabledByDefault: true,
						EntityCategory:   "diagnostic",
						Icon:             "mdi:phone",
						Name:             "Call",
						ObjectID:         fmt.Sprintf("%s_%s", topic, "call"),
						Optimistic:       false,
						Options:          []string{},
						Platform:         "binary_sensor",
						StateTopic:       fmt.Sprintf("%s/%s", prefix, topic),
						UniqueID:         fmt.Sprintf("%s_%s_mutedeck2mqtt", topic, "call"),
						ValueTemplate:    fmt.Sprintf("{{ value_json.%s != 'active' and 'OFF' or 'ON' }}", "call"),
					},
					fmt.Sprintf("%s_%s", topic, "control"): {
						CommandTopic:     "mutedeck2mqtt/no-reply",
						EnabledByDefault: true,
						EntityCategory:   "diagnostic",
						Icon:             "mdi:application-cog",
						Name:             "Control",
						ObjectID:         fmt.Sprintf("%s_%s", topic, "control"),
						Optimistic:       false,
						Options:          []string{"Zoom", "Teams", "Google Meet", "StreamYard", "Webex", "System"},
						Platform:         "select",
						StateTopic:       fmt.Sprintf("%s/%s", prefix, topic),
						UniqueID:         fmt.Sprintf("%s_%s_mutedeck2mqtt", topic, "control"),
						ValueTemplate:    fmt.Sprintf("{{ value_json.%s }}", "control"),
					},
					fmt.Sprintf("%s_%s", topic, "mute"): {
						CommandTopic:     "mutedeck2mqtt/no-reply",
						EnabledByDefault: true,
						EntityCategory:   "diagnostic",
						Icon:             "mdi:microphone",
						Name:             "Microphone",
						ObjectID:         fmt.Sprintf("%s_%s", topic, "mute"),
						Optimistic:       false,
						Options:          []string{},
						Platform:         "binary_sensor",
						StateTopic:       fmt.Sprintf("%s/%s", prefix, topic),
						UniqueID:         fmt.Sprintf("%s_%s_mutedeck2mqtt", topic, "mute"),
						ValueTemplate:    fmt.Sprintf("{{ value_json.%s == 'active' and 'OFF' or 'ON' }}", "mute"),
					},
					fmt.Sprintf("%s_%s", topic, "record"): {
						CommandTopic:     "mutedeck2mqtt/no-reply",
						EnabledByDefault: true,
						EntityCategory:   "diagnostic",
						Icon:             "mdi:record-rec",
						Name:             "Recording",
						ObjectID:         fmt.Sprintf("%s_%s", topic, "record"),
						Optimistic:       false,
						Options:          []string{},
						Platform:         "binary_sensor",
						StateTopic:       fmt.Sprintf("%s/%s", prefix, topic),
						UniqueID:         fmt.Sprintf("%s_%s_mutedeck2mqtt", topic, "record"),
						ValueTemplate:    fmt.Sprintf("{{ value_json.%s != 'active' and 'OFF' or 'ON' }}", "record"),
					},
					fmt.Sprintf("%s_%s", topic, "share"): {
						CommandTopic:     "mutedeck2mqtt/no-reply",
						EnabledByDefault: true,
						EntityCategory:   "diagnostic",
						Icon:             "mdi:monitor-share",
						Name:             "Screen sharing",
						ObjectID:         fmt.Sprintf("%s_%s", topic, "share"),
						Optimistic:       false,
						Options:          []string{},
						Platform:         "binary_sensor",
						StateTopic:       fmt.Sprintf("%s/%s", prefix, topic),
						UniqueID:         fmt.Sprintf("%s_%s_mutedeck2mqtt", topic, "share"),
						ValueTemplate:    fmt.Sprintf("{{ value_json.%s != 'active' and 'OFF' or 'ON' }}", "share"),
					},
					fmt.Sprintf("%s_%s", topic, "video"): {
						CommandTopic:     "mutedeck2mqtt/no-reply",
						EnabledByDefault: true,
						EntityCategory:   "diagnostic",
						Icon:             "mdi:video",
						Name:             "Video",
						ObjectID:         fmt.Sprintf("%s_%s", topic, "video"),
						Optimistic:       false,
						Options:          []string{},
						Platform:         "binary_sensor",
						StateTopic:       fmt.Sprintf("%s/%s", prefix, topic),
						UniqueID:         fmt.Sprintf("%s_%s_mutedeck2mqtt", topic, "video"),
						ValueTemplate:    fmt.Sprintf("{{ value_json.%s != 'active' and 'OFF' or 'ON' }}", "video"),
					},
				},
				StateTopic:       fmt.Sprintf("%s/%s", prefix, topic),
				QualityOfService: 0,
			}
			jsonData, err := json.Marshal(discoveryPayload)
			if err != nil {
				logMessage(ERROR, fmt.Sprintf("Error marshaling discovery JSON data: %v", err))
				http.Error(w, err.Error(), http.StatusInternalServerError)
				mu.Unlock()
				return
			}

			token := client.Publish(discoveryTopic, 0, false, jsonData) // Set retain flag to true for discovery
			token.Wait()
			if token.Error() != nil {
				logMessage(ERROR, fmt.Sprintf("Error publishing discovery message to MQTT topic: %v", token.Error()))
				http.Error(w, token.Error().Error(), http.StatusInternalServerError)
				mu.Unlock()
				return
			}
			logMessage(INFO, fmt.Sprintf("Discovery message sent to topic: %s", discoveryTopic))
			logMessage(DEBUG, fmt.Sprintf("Discovery message body: %s", jsonData))

			discoveryTopics[discoveryTopic] = true
			discoveryMessages[discoveryTopic] = discoveryPayload

			// Pause to give HA time to create the sensors
			time.Sleep(2 * time.Second)
		}
		mu.Unlock()

		// Construct the full MQTT topic
		fullTopic := fmt.Sprintf("%s/%s", prefix, topic)

		// Publish the JSON data to the MQTT topic
		jsonData, err := json.Marshal(data)
		if err != nil {
			logMessage(ERROR, fmt.Sprintf("Error marshaling JSON data: %v", err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		logMessage(DEBUG, fmt.Sprintf("Sending body: %s", jsonData))
		token := client.Publish(fullTopic, 0, false, jsonData)
		token.Wait()
		if token.Error() != nil {
			logMessage(ERROR, fmt.Sprintf("Error publishing to MQTT topic: %v", token.Error()))
			http.Error(w, token.Error().Error(), http.StatusInternalServerError)
			return
		}

		// Log the published message
		logMessage(INFO, fmt.Sprintf("MQT: %s = %s", fullTopic, string(jsonData)))

		w.WriteHeader(http.StatusOK)
	})

	// Get the port from environment variable or use default
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Start the HTTP server
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%s", port), nil))
}

func resendDiscoveryMessages(client mqtt.Client) {
	mu.Lock()
	defer mu.Unlock()
	for topic, payload := range discoveryMessages {
		jsonData, err := json.Marshal(payload)
		if err != nil {
			logMessage(ERROR, fmt.Sprintf("Error marshaling discovery JSON data: %v", err))
			continue
		}

		token := client.Publish(topic, 0, false, jsonData)
		token.Wait()
		if token.Error() != nil {
			logMessage(ERROR, fmt.Sprintf("Error publishing discovery message to MQTT topic: %v", token.Error()))
			continue
		}
		logMessage(INFO, fmt.Sprintf("Resent discovery message to topic: %s", topic))
		logMessage(DEBUG, fmt.Sprintf("Resent discovery message body: %s", jsonData))
	}
}
