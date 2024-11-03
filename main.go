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
const object_id = "mutedeck2mqtt_device_"

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

// Function to get the icon based on the key
func getIcon(key string) string {
	switch key {
	case "call":
		return "mdi:phone"
	case "control":
		return "mdi:application-cog"
	case "mute":
		return "mdi:microphone-off"
	case "record":
		return "mdi:record-rec"
	case "share":
		return "mdi:monitor-share"
	case "video":
		return "mdi:video"
	default:
		return "mdi:information-outline"
	}
}

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
		discoveryTopic := fmt.Sprintf("%s/%s%s/%s/config", discovery_prefix, component, object_id, topic)
		mu.Lock()
		if !discoveryTopics[discoveryTopic] {
			discoveryPayload := map[string]interface{}{
				"device": map[string]interface{}{
					"identifiers":  []string{fmt.Sprintf("%s_%s", object_id, topic)},
					"manufacturer": "MuteDeck",
					"name":         topic,
					"via_device":   fmt.Sprintf("%s_%s", object_id, topic),
				},
				"enabled_by_default": true,
				"entity_category":    "diagnostic",
				"icon":               "mdi:phone", // Default icon, will be updated per key
				"object_id":          fmt.Sprintf("%s_%s", topic, "key"),
				"origin": map[string]interface{}{
					"name": "MuteDeck2MQTT",
					"sw":   "2024.11.01",
					"url":  "https://github.com/chelming/mutedeck2mqtt",
				},
				"state_topic":    fmt.Sprintf("%s/%s", prefix, topic),
				"unique_id":      fmt.Sprintf("%s_%s_mutedeck2mqtt", topic, "key"),
				"value_template": "{{ value_json.key }}",
			}

			for key, _ := range data {
				icon := getIcon(key)

				discoveryPayload["icon"] = icon
				discoveryPayload["object_id"] = fmt.Sprintf("%s_%s", topic, key)
				discoveryPayload["unique_id"] = fmt.Sprintf("%s_%s_mutedeck2mqtt", topic, key)
				discoveryPayload["value_template"] = fmt.Sprintf("{{ value_json.%s }}", key)

				jsonData, err := json.Marshal(discoveryPayload)
				if err != nil {
					logMessage(ERROR, fmt.Sprintf("Error marshaling discovery JSON data: %v", err))
					http.Error(w, err.Error(), http.StatusInternalServerError)
					mu.Unlock()
					return
				}

				token := client.Publish(discoveryTopic, 0, true, jsonData) // Set retain flag to true for discovery
				token.Wait()
				if token.Error() != nil {
					logMessage(ERROR, fmt.Sprintf("Error publishing discovery message to MQTT topic: %v", token.Error()))
					http.Error(w, token.Error().Error(), http.StatusInternalServerError)
					mu.Unlock()
					return
				}
				discoveryTopics[discoveryTopic] = true
				logMessage(INFO, fmt.Sprintf("Discovery message sent to topic: %s", discoveryTopic))
			}
		}
		mu.Unlock()

		// Construct the full MQTT topic
		fullTopic := ""
		if prefix != "" {
			fullTopic += prefix + "/"
		}
		fullTopic += topic + "/tele/SENSOR"

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
		logMessage(INFO, fmt.Sprintf("MQT: %s = %s", topic+"/tele/SENSOR", string(jsonData)))

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
