package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// MuteDeck's /v1 HTTP API (see http://localhost:3491/swagger-ui on the MuteDeck host).
// Only used when MUTEDECK_API_URL is set; without it the bridge is webhook-only.
type muteDeckAPI struct {
	baseURL string
	token   string
	client  *http.Client
}

// Settings for the one MuteDeck instance the API controls
var api *muteDeckAPI
var apiPrefix = "mutedeck2mqtt"
var apiTopic = "mutedeck"

// Last availability published for the API device, nil until the first poll
var apiAvailable *bool
var availMu sync.Mutex

func newMuteDeckAPI(baseURL, token string) *muteDeckAPI {
	return &muteDeckAPI{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		client:  &http.Client{Timeout: 3 * time.Second},
	}
}

func (a *muteDeckAPI) do(method, path string, query url.Values) ([]byte, error) {
	u := a.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequest(method, u, nil)
	if err != nil {
		return nil, err
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return body, fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func (a *muteDeckAPI) status() (map[string]interface{}, error) {
	body, err := a.do(http.MethodGet, "/v1/status", nil)
	if err != nil {
		return nil, err
	}
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	return data, nil
}

// action posts to /v1/<endpoint>. MuteDeck answers HTTP 200 with a body status of 400
// when it cannot act (for example video outside a call), so the body is checked too.
func (a *muteDeckAPI) action(endpoint, state string) error {
	query := url.Values{}
	if state != "" {
		query.Set("state", state)
	}
	body, err := a.do(http.MethodPost, "/v1/"+endpoint, query)
	if err != nil {
		return err
	}
	var result struct {
		Status  int    `json:"status"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &result) == nil && result.Status != 0 && result.Status != http.StatusOK {
		return fmt.Errorf("MuteDeck refused %s: %d %s %s", endpoint, result.Status, result.Reason, result.Message)
	}
	return nil
}

// commandFor maps an MQTT command to a MuteDeck API call and the state MuteDeck should
// report afterwards. The Microphone switch is ON when the mic is live, so ON unmutes.
func commandFor(field, payload string) (endpoint, state string, predicted map[string]interface{}, err error) {
	payload = strings.ToUpper(strings.TrimSpace(payload))
	switch field {
	case "mute":
		switch payload {
		case "ON":
			return "mute", "off", map[string]interface{}{"mute": "inactive"}, nil
		case "OFF":
			return "mute", "on", map[string]interface{}{"mute": "active"}, nil
		case "TOGGLE":
			return "mute", "toggle", nil, nil
		}
	case "video":
		switch payload {
		case "ON":
			return "video", "on", map[string]interface{}{"video": "active"}, nil
		case "OFF":
			return "video", "off", map[string]interface{}{"video": "inactive"}, nil
		case "TOGGLE":
			return "video", "toggle", nil, nil
		}
	case "leave":
		if payload == "PRESS" || payload == "" {
			return "leave", "", nil, nil
		}
	default:
		return "", "", nil, fmt.Errorf("unknown command %q", field)
	}
	return "", "", nil, fmt.Errorf("unknown payload %q for %s", payload, field)
}

func apiStateTopic() string {
	return fmt.Sprintf("%s/%s", apiPrefix, apiTopic)
}

func apiAvailabilityTopic() string {
	return fmt.Sprintf("%s/%s/availability", apiPrefix, apiTopic)
}

func bridgeStatusTopic() string {
	return fmt.Sprintf("%s/status", apiPrefix)
}

// subscribeCommands listens for switch and button commands from Home Assistant.
// Called from the MQTT on-connect handler, so subscriptions survive reconnects.
func subscribeCommands(client mqtt.Client, discoveryPrefix string) {
	handler := func(client mqtt.Client, msg mqtt.Message) {
		// <prefix>/<topic>/<field>/set or <prefix>/<topic>/leave
		field := strings.TrimSuffix(strings.TrimPrefix(msg.Topic(), apiStateTopic()+"/"), "/set")
		go handleCommand(client, discoveryPrefix, field, string(msg.Payload()))
	}
	for _, topic := range []string{apiStateTopic() + "/+/set", apiStateTopic() + "/leave"} {
		if token := client.Subscribe(topic, 0, handler); token.Wait() && token.Error() != nil {
			logMessage(ERROR, fmt.Sprintf("Error subscribing to %s: %v", topic, token.Error()))
		} else {
			logMessage(INFO, fmt.Sprintf("Listening for commands on %s", topic))
		}
	}
}

func handleCommand(client mqtt.Client, discoveryPrefix, field, payload string) {
	endpoint, state, predicted, err := commandFor(field, payload)
	if err != nil {
		logMessage(WARN, fmt.Sprintf("Ignoring command: %v", err))
		return
	}

	// A toggle or leave has no predicted state, so it is confirmed by watching a field
	// change. Read the baseline now, before acting: lastState may be empty or stale.
	var before map[string]interface{}
	if predicted == nil {
		if before, err = api.status(); err != nil {
			logMessage(WARN, fmt.Sprintf("Could not read MuteDeck status before %s: %v", field, err))
		}
	}

	logMessage(INFO, fmt.Sprintf("Command %s=%s -> /v1/%s state=%q", field, payload, endpoint, state))
	if err := api.action(endpoint, state); err != nil {
		logMessage(ERROR, fmt.Sprintf("Command %s=%s failed: %v", field, payload, err))
		// Publish what MuteDeck really reports so HA drops back from the requested state
		refreshStatus(client, discoveryPrefix, false)
		return
	}

	if predicted == nil && before == nil {
		// Without a baseline any status would look settled, so just publish the current one
		refreshStatus(client, discoveryPrefix, false)
		return
	}

	// Show the expected state at once instead of waiting for the webhook or the next
	// poll. MuteDeck's /v1/status lags the change by up to ~1.5 s, so publishing it
	// straight away would flip the switch back for a moment. Instead, wait until
	// MuteDeck agrees (or the window runs out) and then publish what it reports.
	id := startConfirm(predicted)
	defer finishConfirm(id)
	if predicted != nil {
		if err := publishState(client, discoveryPrefix, apiPrefix, apiTopic, predicted, false); err != nil {
			logMessage(ERROR, fmt.Sprintf("Error publishing predicted state: %v", err))
		}
	}
	confirmCommand(client, discoveryPrefix, id, watchedField(field), predicted, before)
}

// watchedField is the status field a command without a prediction is expected to change
func watchedField(field string) string {
	if field == "leave" {
		return "call"
	}
	return field
}

// Commands being confirmed, by id, with their predicted state (nil for a toggle).
// Polls skip publishing while any is in flight, so a stale status cannot overwrite
// a predicted state. A confirmed status keeps the predictions of the other commands
// still in flight, so confirming one command cannot undo another.
var pending = map[int]map[string]interface{}{}
var pendingSeq int
var confirmMu sync.Mutex

func confirming() bool {
	confirmMu.Lock()
	defer confirmMu.Unlock()
	return len(pending) > 0
}

func startConfirm(predicted map[string]interface{}) int {
	confirmMu.Lock()
	defer confirmMu.Unlock()
	pendingSeq++
	pending[pendingSeq] = predicted
	return pendingSeq
}

// finishConfirm removes the command and returns the predictions of those still in
// flight. Calling it again for the same id is harmless.
func finishConfirm(id int) map[string]interface{} {
	confirmMu.Lock()
	defer confirmMu.Unlock()
	delete(pending, id)
	others := map[string]interface{}{}
	for _, predicted := range pending {
		for k, v := range predicted {
			others[k] = v
		}
	}
	return others
}

func confirmCommand(client mqtt.Client, discoveryPrefix string, id int, field string, predicted, before map[string]interface{}) {
	const attempts = 6
	const interval = 500 * time.Millisecond

	for i := 1; i <= attempts; i++ {
		time.Sleep(interval)
		data, err := api.status()
		if err != nil {
			continue
		}
		settled := true
		if predicted != nil {
			for k, v := range predicted {
				if data[k] != v {
					settled = false
				}
			}
		} else {
			settled = data[field] != before[field]
		}
		if settled || i == attempts {
			if !settled {
				logMessage(WARN, fmt.Sprintf("MuteDeck did not confirm %s within %s, publishing what it reports", field, attempts*interval))
			}
			for k, v := range finishConfirm(id) {
				data[k] = v
			}
			if err := publishState(client, discoveryPrefix, apiPrefix, apiTopic, data, true); err != nil {
				logMessage(ERROR, fmt.Sprintf("Error publishing confirmed state: %v", err))
			}
			return
		}
	}
	// Every attempt failed to reach MuteDeck; the poller takes over availability
}

// refreshStatus reads /v1/status, publishes it and updates availability
func refreshStatus(client mqtt.Client, discoveryPrefix string, onlyIfChanged bool) {
	data, err := api.status()
	if err != nil {
		if setAvailable(client, false) {
			logMessage(WARN, fmt.Sprintf("MuteDeck unreachable, marking offline: %v", err))
		}
		return
	}
	if setAvailable(client, true) {
		logMessage(INFO, "MuteDeck reachable, marking online")
	}
	if err := publishState(client, discoveryPrefix, apiPrefix, apiTopic, data, onlyIfChanged); err != nil {
		logMessage(ERROR, fmt.Sprintf("Error publishing polled state: %v", err))
	}
}

// setAvailable publishes the API device's availability, retained. It reports whether
// the value changed.
func setAvailable(client mqtt.Client, available bool) bool {
	availMu.Lock()
	defer availMu.Unlock()
	if apiAvailable != nil && *apiAvailable == available {
		return false
	}
	payload := "offline"
	if available {
		payload = "online"
	}
	token := client.Publish(apiAvailabilityTopic(), 1, true, payload)
	token.Wait()
	if token.Error() != nil {
		logMessage(ERROR, fmt.Sprintf("Error publishing availability: %v", token.Error()))
		return false
	}
	apiAvailable = &available
	return true
}

// republishAvailability restores the retained availability after an MQTT reconnect
func republishAvailability(client mqtt.Client) {
	availMu.Lock()
	defer availMu.Unlock()
	if apiAvailable == nil {
		return
	}
	payload := "offline"
	if *apiAvailable {
		payload = "online"
	}
	client.Publish(apiAvailabilityTopic(), 1, true, payload)
}

func pollStatus(client mqtt.Client, discoveryPrefix string, interval time.Duration) {
	for {
		if !confirming() {
			refreshStatus(client, discoveryPrefix, true)
		}
		time.Sleep(interval)
	}
}
