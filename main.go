package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
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

type Availability struct {
	Topic         string `json:"t"`
	ValueTemplate string `json:"val_tpl,omitempty"`
}

type Component struct {
	CommandTopic     string         `json:"cmd_t,omitempty"`
	EnabledByDefault bool           `json:"en"`
	EntityCategory   string         `json:"ent_cat,omitempty"`
	Icon             string         `json:"icon"`
	Name             string         `json:"name"`
	ObjectID         string         `json:"obj_id"`
	Optimistic       bool           `json:"opt"`
	Options          []string       `json:"options,omitempty"`
	Platform         string         `json:"p"`
	StateTopic       string         `json:"stat_t,omitempty"`
	UniqueID         string         `json:"uniq_id"`
	ValueTemplate    string         `json:"val_tpl,omitempty"`
	Availability     []Availability `json:"avty,omitempty"`
	AvailabilityMode string         `json:"avty_mode,omitempty"`
}

type DiscoveryPayloadStruct struct {
	Device           Device               `json:"dev"`
	Origin           Origin               `json:"o"`
	Components       map[string]Component `json:"cmps"`
	StateTopic       string               `json:"stat_t"`
	QualityOfService int                  `json:"qos"`
	Availability     []Availability       `json:"avty,omitempty"`
	AvailabilityMode string               `json:"avty_mode,omitempty"`
}

// The six fields MuteDeck reports, both in its webhook and in /v1/status
var stateFields = []string{"call", "control", "mute", "record", "share", "video"}

// Last state published per state topic, to merge partial updates and skip repeats
var lastState = make(map[string]map[string]interface{})
var lastPayload = make(map[string]string)
var stateMu sync.Mutex

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

	// Optional MuteDeck API, which makes the device controllable from Home Assistant
	if apiURL := os.Getenv("MUTEDECK_API_URL"); apiURL != "" {
		api = newMuteDeckAPI(apiURL, os.Getenv("MUTEDECK_API_TOKEN"))
		if v := os.Getenv("MUTEDECK_PREFIX"); v != "" {
			apiPrefix = v
		}
		if v := os.Getenv("MUTEDECK_TOPIC"); v != "" {
			apiTopic = v
		}
		logMessage(INFO, fmt.Sprintf("Controlling MuteDeck at %s as %s", api.baseURL, apiStateTopic()))
	}
	pollInterval := 5 * time.Second
	if v := os.Getenv("MUTEDECK_POLL_INTERVAL"); v != "" {
		seconds, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || (seconds > 0 && seconds < 0.5) {
			log.Fatalf("Invalid MUTEDECK_POLL_INTERVAL: %q (0 disables polling, otherwise at least 0.5 seconds)", v)
		}
		pollInterval = time.Duration(seconds * float64(time.Second))
	}

	// MQTT client options
	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://%s:%d", MQTT_HOST, MQTT_PORT))
	opts.SetClientID(clientID)
	opts.SetUsername(MQTT_USER)
	opts.SetPassword(MQTT_PASS)
	opts.SetAutoReconnect(true)
	// Marks every entity of the API device unavailable when the bridge goes away.
	// Webhook-only devices have no availability, so they get no status topic.
	if api != nil {
		opts.SetWill(bridgeStatusTopic(), "offline", 1, true)
	}

	// Subscriptions are made here so they are restored after a reconnect
	opts.SetOnConnectHandler(func(client mqtt.Client) {
		logMessage(INFO, "Connected to MQTT")
		if api != nil {
			client.Publish(bridgeStatusTopic(), 1, true, "online")
		}

		// Subscribe to homeassistant/status topic
		client.Subscribe("homeassistant/status", 0, func(client mqtt.Client, msg mqtt.Message) {
			if string(msg.Payload()) == "online" {
				logMessage(INFO, "Home Assistant is online, resending discovery message")
				resendDiscoveryMessages(client)
			}
		})

		if api != nil {
			subscribeCommands(client, discovery_prefix)
			republishAvailability(client)
		}
	})

	// Create and start the MQTT client
	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		log.Fatal(token.Error())
	}

	if api != nil {
		if pollInterval > 0 {
			go pollStatus(client, discovery_prefix, pollInterval)
		} else {
			// Without polling there is nothing to judge reachability by
			setAvailable(client, true)
		}
	}

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

		// Get MQTT topic and prefix from URL parameters
		topic := r.URL.Query().Get("topic")
		if topic == "" {
			topic = "mutedeck"
		}
		prefix := r.URL.Query().Get("prefix")
		if prefix == "" {
			prefix = "mutedeck2mqtt"
		}

		if err := publishState(client, discovery_prefix, prefix, topic, data, false); err != nil {
			logMessage(ERROR, fmt.Sprintf("Error publishing webhook state: %v", err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

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

// isAPIDevice reports whether prefix/topic is the device the MuteDeck API controls
func isAPIDevice(prefix, topic string) bool {
	return api != nil && prefix == apiPrefix && topic == apiTopic
}

// binarySensor builds a read-only component on the shared state topic
func binarySensor(topic, field, name, icon, valueTemplate string) Component {
	return Component{
		CommandTopic:     "mutedeck2mqtt/no-reply",
		EnabledByDefault: true,
		EntityCategory:   "diagnostic",
		Icon:             icon,
		Name:             name,
		ObjectID:         fmt.Sprintf("%s_%s", topic, field),
		Optimistic:       false,
		Options:          []string{},
		Platform:         "binary_sensor",
		StateTopic:       "",
		UniqueID:         fmt.Sprintf("%s_%s_mutedeck2mqtt", topic, field),
		ValueTemplate:    valueTemplate,
	}
}

func buildDiscovery(prefix, topic string) DiscoveryPayloadStruct {
	stateTopic := fmt.Sprintf("%s/%s", prefix, topic)
	components := map[string]Component{}

	call := binarySensor(topic, "call", "Call", "mdi:phone", "{{ value_json.call != 'active' and 'OFF' or 'ON' }}")
	control := binarySensor(topic, "control", "Control", "mdi:application-cog", "{{ value_json.control }}")
	control.Platform = "select"
	control.Options = []string{"Zoom", "Teams", "Google Meet", "StreamYard", "Webex", "System"}
	mute := binarySensor(topic, "mute", "Microphone", "mdi:microphone", "{{ value_json.mute == 'active' and 'OFF' or 'ON' }}")
	record := binarySensor(topic, "record", "Recording", "mdi:record-rec", "{{ value_json.record != 'active' and 'OFF' or 'ON' }}")
	share := binarySensor(topic, "share", "Screen sharing", "mdi:monitor-share", "{{ value_json.share != 'active' and 'OFF' or 'ON' }}")
	video := binarySensor(topic, "video", "Video", "mdi:video", "{{ value_json.video != 'active' and 'OFF' or 'ON' }}")

	payload := DiscoveryPayloadStruct{
		Device: Device{
			IDs:          []string{fmt.Sprintf("%s_%s", object_id, topic)},
			Name:         toTitleCase(topic),
			Manufacturer: "MuteDeck",
		},
		Origin: Origin{
			Name:            "MuteDeck2MQTT",
			SoftwareVersion: "2026.10.29",
			URL:             "https://github.com/chelming/mutedeck2mqtt/",
		},
		StateTopic:       stateTopic,
		QualityOfService: 0,
	}

	if isAPIDevice(prefix, topic) {
		// Controllable: Microphone and Video become switches, Leave meeting a button.
		// The device is available only while both the bridge and MuteDeck are reachable.
		deviceAvailability := []Availability{{Topic: bridgeStatusTopic()}, {Topic: apiAvailabilityTopic()}}
		payload.Availability = deviceAvailability
		payload.AvailabilityMode = "all"
		withState := func(template string) []Availability {
			return append(append([]Availability{}, deviceAvailability...), Availability{Topic: stateTopic, ValueTemplate: template})
		}

		call.EntityCategory = ""
		call.CommandTopic = ""

		mute.Platform = "switch"
		mute.EntityCategory = ""
		mute.CommandTopic = fmt.Sprintf("%s/mute/set", stateTopic)
		mute.Icon = "mdi:microphone"

		video.Platform = "switch"
		video.EntityCategory = ""
		video.CommandTopic = fmt.Sprintf("%s/video/set", stateTopic)
		// MuteDeck reports video "disabled" outside a call, when there is no camera to switch
		video.Availability = withState("{{ 'online' if value_json.video in ['active', 'inactive'] else 'offline' }}")
		video.AvailabilityMode = "all"

		components[fmt.Sprintf("%s_%s", topic, "leave")] = Component{
			CommandTopic:     fmt.Sprintf("%s/leave", stateTopic),
			EnabledByDefault: true,
			Icon:             "mdi:phone-hangup",
			Name:             "Leave meeting",
			ObjectID:         fmt.Sprintf("%s_%s", topic, "leave"),
			Platform:         "button",
			UniqueID:         fmt.Sprintf("%s_%s_mutedeck2mqtt", topic, "leave"),
			Availability:     withState("{{ 'online' if value_json.call == 'active' else 'offline' }}"),
			AvailabilityMode: "all",
		}
	}

	for field, component := range map[string]Component{"call": call, "control": control, "mute": mute, "record": record, "share": share, "video": video} {
		component.StateTopic = stateTopic
		components[fmt.Sprintf("%s_%s", topic, field)] = component
	}
	payload.Components = components
	return payload
}

// ensureDiscovery publishes the device's discovery message the first time it is seen
func ensureDiscovery(client mqtt.Client, discoveryPrefix, prefix, topic string) error {
	sent, err := sendDiscovery(client, discoveryPrefix, prefix, topic)
	if sent {
		// Pause to give HA time to create the sensors. This runs after mu is released so
		// the poller, commands, webhooks and discovery resends are not held up meanwhile;
		// any state they publish is retained, so HA still picks it up.
		time.Sleep(2 * time.Second)
	}
	return err
}

// sendDiscovery publishes the discovery message unless it was already sent, and reports
// whether it sent one now
func sendDiscovery(client mqtt.Client, discoveryPrefix, prefix, topic string) (bool, error) {
	discoveryTopic := fmt.Sprintf("%s/%s/%s_%s/config", discoveryPrefix, "device", object_id, topic)
	mu.Lock()
	defer mu.Unlock()
	if discoveryTopics[discoveryTopic] {
		return false, nil
	}

	logMessage(DEBUG, "Preparing discovery topic")
	discoveryPayload := buildDiscovery(prefix, topic)
	jsonData, err := json.Marshal(discoveryPayload)
	if err != nil {
		return false, fmt.Errorf("marshaling discovery JSON data: %w", err)
	}

	token := client.Publish(discoveryTopic, 0, true, jsonData) // Retain so HA receives discovery on reconnect
	if err := waitToken(token); err != nil {
		return false, fmt.Errorf("publishing discovery message: %w", err)
	}
	logMessage(INFO, fmt.Sprintf("Discovery message sent to topic: %s", discoveryTopic))
	logMessage(DEBUG, fmt.Sprintf("Discovery message body: %s", jsonData))

	discoveryTopics[discoveryTopic] = true
	discoveryMessages[discoveryTopic] = discoveryPayload
	return true, nil
}

// publishState merges data into the device's last known state and publishes it, retained.
// Webhook posts, polled /v1/status and predicted command results all come through here.
// With onlyIfChanged, a state identical to the last one published is skipped.
func publishState(client mqtt.Client, discoveryPrefix, prefix, topic string, data map[string]interface{}, onlyIfChanged bool) error {
	if err := ensureDiscovery(client, discoveryPrefix, prefix, topic); err != nil {
		return err
	}

	// Construct the full MQTT topic
	fullTopic := fmt.Sprintf("%s/%s", prefix, topic)

	stateMu.Lock()
	defer stateMu.Unlock()
	state := map[string]interface{}{}
	for k, v := range lastState[fullTopic] {
		state[k] = v
	}
	for _, key := range stateFields {
		if v, ok := data[key]; ok {
			// Map only the incoming control value: the stored one is already mapped
			// and getPlatformName is not idempotent
			if control, isString := v.(string); isString && key == "control" {
				v = getPlatformName(control)
			}
			state[key] = v
		}
	}

	jsonData, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshaling JSON data: %w", err)
	}
	if onlyIfChanged && lastPayload[fullTopic] == string(jsonData) {
		return nil
	}

	logMessage(DEBUG, fmt.Sprintf("Sending body: %s", jsonData))
	token := client.Publish(fullTopic, 0, true, jsonData)
	if err := waitToken(token); err != nil {
		return fmt.Errorf("publishing to MQTT topic: %w", err)
	}
	lastState[fullTopic] = state
	lastPayload[fullTopic] = string(jsonData)

	// Log the published message
	logMessage(INFO, fmt.Sprintf("MQT: %s = %s", fullTopic, string(jsonData)))
	return nil
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

		token := client.Publish(topic, 0, true, jsonData)
		if err := waitToken(token); err != nil {
			logMessage(ERROR, fmt.Sprintf("Error publishing discovery message to MQTT topic: %v", err))
			continue
		}
		logMessage(INFO, fmt.Sprintf("Resent discovery message to topic: %s", topic))
		logMessage(DEBUG, fmt.Sprintf("Resent discovery message body: %s", jsonData))
	}
}

// waitToken waits for a publish to complete, giving up after a timeout so a stalled
// broker cannot block the poller, webhooks and commands forever
func waitToken(token mqtt.Token) error {
	if !token.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("timed out waiting for the MQTT broker")
	}
	return token.Error()
}
