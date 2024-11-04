package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Custom log levels
const (
	DEBUG = iota
	INFO
	WARN
	ERROR
)

const component = "sensor"
const object_id = "mutedeck2mqtt_device"

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

// Function to get the icon and options based on the key
func getIconAndOptions(key string) (string, []string) {
	var icon string
	var options []string
	switch key {
	case "call":
		icon = "mdi:phone"
		options = []string{"inactive", "active"}
	case "control":
		icon = "mdi:application-cog"
		options = []string{"zoom", "teams", "google-meet", "system"}
	case "mute":
		icon = "mdi:microphone-off"
		options = []string{"active", "inactive", "disabled"}
	case "record":
		icon = "mdi:record-rec"
		options = []string{"active", "inactive", "disabled"}
	case "share":
		icon = "mdi:monitor-share"
		options = []string{"active", "inactive", "disabled"}
	case "video":
		icon = "mdi:video"
		options = []string{"active", "inactive", "disabled"}
	default:
		icon = "mdi:information-outline"
		options = []string{}
	}
	return icon, options
}

func toSentenceCase(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	caser := cases.Title(language.English)
	return caser.String(s)
}

// DiscoveryPayload struct
type DiscoveryPayload struct {
	Device struct {
		Identifiers  []string `json:"identifiers"`
		Manufacturer string   `json:"manufacturer"`
		Name         string   `json:"name"`
		ViaDevice    string   `json:"via_device"`
	} `json:"device"`
	DeviceClass      string `json:"device_class"`
	EnabledByDefault bool   `json:"enabled_by_default"`
	EntityCategory   string `json:"entity_category"`
	Icon             string `json:"icon"`
	Name             string `json:"name"`
	ObjectID         string `json:"object_id"`
	Origin           struct {
		Name string `json:"name"`
		SW   string `json:"sw"`
		URL  string `json:"url"`
	} `json:"origin"`
	StateTopic    string   `json:"state_topic"`
	UniqueID      string   `json:"unique_id"`
	ValueTemplate string   `json:"value_template"`
	Options       []string `json:"options"`
}

var discoveryMessages = make(map[string]DiscoveryPayload)

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
			logMessage(INFO, "Home Assistant is online, resending discovery messages")
			resendDiscoveryMessages(client)
		}
	})

	// HTTP server handler
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Get the client's IP address
		clientIP := getClientIP(r)
		logMessage(DEBUG, fmt.Sprintf("Request received from IP: %s", clientIP))

		// Parse JSON body
		var data map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&data)
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

		// Get MQTT topic and prefix from URL parameters
		topic := r.URL.Query().Get("topic")
		if topic == "" {
			topic = "mutedeck"
		}
		prefix := r.URL.Query().Get("prefix")
		if prefix == "" {
			prefix = "mutedeck2mqtt"
		}

		// Publish the discovery message if not already sent
		keysToSend := []string{"record", "share", "video", "call", "control", "mute"}
		for _, key := range keysToSend {
			if _, ok := data[key]; ok {
				icon, options := getIconAndOptions(key)
				discoveryPayload := DiscoveryPayload{
					DeviceClass:      "enum",
					EnabledByDefault: true,
					EntityCategory:   "diagnostic",
					Icon:             icon,
					Name:             toSentenceCase(key),
					ObjectID:         fmt.Sprintf("%s_%s", topic, key),
					StateTopic:       fmt.Sprintf("%s/%s", prefix, topic),
					UniqueID:         fmt.Sprintf("%s_%s_mutedeck2mqtt", topic, key),
					ValueTemplate:    fmt.Sprintf("{{ value_json.%s }}", key),
					Options:          options,
				}
				discoveryPayload.Device.Identifiers = []string{fmt.Sprintf("%s_%s", object_id, topic)}
				discoveryPayload.Device.Manufacturer = "MuteDeck"
				discoveryPayload.Device.Name = toSentenceCase(topic)
				discoveryPayload.Device.ViaDevice = fmt.Sprintf("%s_%s", object_id, topic)
				discoveryPayload.Origin.Name = "MuteDeck2MQTT"
				discoveryPayload.Origin.SW = "2024.11.01"
				discoveryPayload.Origin.URL = "https://github.com/chelming/mutedeck2mqtt"

				discoveryTopic := fmt.Sprintf("%s/%s/%s_%s/%s/config", discovery_prefix, component, object_id, topic, key)

				mu.Lock()
				if !discoveryTopics[discoveryTopic] {
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
				}
				mu.Unlock()
			}
		}

		// Construct the full MQTT topic
		fullTopic := ""
		if prefix != "" {
			fullTopic += prefix + "/"
		}
		fullTopic += topic

		// Publish the JSON data to the MQTT topic
		jsonData, err := json.Marshal(data)
		if err != nil {
			logMessage(ERROR, fmt.Sprintf("Error marshaling JSON data: %v", err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		logMessage(DEBUG, fmt.Sprintf("Received body from %s: %s", clientIP, jsonData))

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
