package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCommandFor(t *testing.T) {
	cases := []struct {
		field, payload    string
		endpoint, state   string
		predictKey, value string
	}{
		// The Microphone switch is ON when the mic is live, so ON means "mute off"
		{"mute", "ON", "mute", "off", "mute", "inactive"},
		{"mute", "off", "mute", "on", "mute", "active"},
		{"mute", "TOGGLE", "mute", "toggle", "", ""},
		{"video", "ON", "video", "on", "video", "active"},
		{"video", "OFF", "video", "off", "video", "inactive"},
		{"leave", "PRESS", "leave", "", "", ""},
	}
	for _, c := range cases {
		endpoint, state, predicted, err := commandFor(c.field, c.payload)
		if err != nil {
			t.Fatalf("%s=%s: unexpected error %v", c.field, c.payload, err)
		}
		if endpoint != c.endpoint || state != c.state {
			t.Errorf("%s=%s: got /v1/%s state=%q, want /v1/%s state=%q", c.field, c.payload, endpoint, state, c.endpoint, c.state)
		}
		if c.predictKey == "" && predicted != nil {
			t.Errorf("%s=%s: predicted %v, want none", c.field, c.payload, predicted)
		}
		if c.predictKey != "" && predicted[c.predictKey] != c.value {
			t.Errorf("%s=%s: predicted %v, want %s=%s", c.field, c.payload, predicted, c.predictKey, c.value)
		}
	}
	for _, bad := range [][2]string{{"mute", "MAYBE"}, {"share", "ON"}, {"leave", "NOW"}} {
		if _, _, _, err := commandFor(bad[0], bad[1]); err == nil {
			t.Errorf("%s=%s: expected an error", bad[0], bad[1])
		}
	}
}

// fakeMuteDeck answers like MuteDeck's /v1 API and records the last action request
func fakeMuteDeck(t *testing.T, token string, actionStatus int) (*httptest.Server, *http.Request) {
	last := &http.Request{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"status":401,"reason":"not_authenticated"}`))
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/status":
			w.Write([]byte(`{"call":"active","control":"google-meet","mute":"active","record":"inactive","share":"inactive","status":200,"video":"inactive"}`))
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/"):
			*last = *r
			json.NewEncoder(w).Encode(map[string]interface{}{"status": actionStatus, "reason": "not_in_call"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, last
}

func TestAPIAction(t *testing.T) {
	server, last := fakeMuteDeck(t, "secret", 200)
	a := newMuteDeckAPI(server.URL+"/", "secret")
	if err := a.action("mute", "off"); err != nil {
		t.Fatalf("action: %v", err)
	}
	if last.URL.Path != "/v1/mute" || last.URL.Query().Get("state") != "off" {
		t.Errorf("got %s?%s, want /v1/mute?state=off", last.URL.Path, last.URL.RawQuery)
	}
	if err := a.action("leave", ""); err != nil || last.URL.RawQuery != "" {
		t.Errorf("leave: err=%v query=%q", err, last.URL.RawQuery)
	}
}

func TestAPIActionRefused(t *testing.T) {
	// HTTP 200 with a body status of 400 is MuteDeck saying it could not act
	server, _ := fakeMuteDeck(t, "secret", 400)
	if err := newMuteDeckAPI(server.URL, "secret").action("video", "on"); err == nil || !strings.Contains(err.Error(), "not_in_call") {
		t.Errorf("got %v, want a not_in_call error", err)
	}
}

func TestAPIStatusAndAuth(t *testing.T) {
	server, _ := fakeMuteDeck(t, "secret", 200)
	data, err := newMuteDeckAPI(server.URL, "secret").status()
	if err != nil || data["call"] != "active" || data["control"] != "google-meet" {
		t.Errorf("status: %v %v", data, err)
	}
	if _, err := newMuteDeckAPI(server.URL, "wrong").status(); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("bad token: got %v, want HTTP 401", err)
	}
}

func TestBuildDiscovery(t *testing.T) {
	defer func() { api = nil }()

	// Webhook-only: everything stays a read-only sensor, as before
	api = nil
	d := buildDiscovery("mutedeck2mqtt", "work")
	if p := d.Components["work_mute"].Platform; p != "binary_sensor" {
		t.Errorf("webhook-only mute platform %q, want binary_sensor", p)
	}
	if _, ok := d.Components["work_leave"]; ok || d.Availability != nil {
		t.Errorf("webhook-only device should have no leave button and no availability")
	}

	// API device: switches, a button, and availability on bridge + MuteDeck
	api = newMuteDeckAPI("http://mutedeck", "")
	apiPrefix, apiTopic = "mutedeck2mqtt", "work"
	d = buildDiscovery("mutedeck2mqtt", "work")
	for field, want := range map[string]string{"mute": "switch", "video": "switch", "leave": "button", "call": "binary_sensor", "share": "binary_sensor"} {
		c := d.Components["work_"+field]
		if c.Platform != want {
			t.Errorf("%s platform %q, want %q", field, c.Platform, want)
		}
	}
	if c := d.Components["work_mute"]; c.CommandTopic != "mutedeck2mqtt/work/mute/set" || c.EntityCategory != "" {
		t.Errorf("mute switch: %+v", c)
	}
	if c := d.Components["work_leave"]; c.CommandTopic != "mutedeck2mqtt/work/leave" || len(c.Availability) != 3 {
		t.Errorf("leave button: %+v", c)
	}
	if len(d.Availability) != 2 || d.AvailabilityMode != "all" {
		t.Errorf("device availability: %+v %q", d.Availability, d.AvailabilityMode)
	}

	// A different topic on the same bridge is still webhook-only
	if p := buildDiscovery("mutedeck2mqtt", "other").Components["other_mute"].Platform; p != "binary_sensor" {
		t.Errorf("other topic mute platform %q, want binary_sensor", p)
	}

	// The button has no state topic; everything else reads the shared one
	if d.Components["work_leave"].StateTopic != "" || d.Components["work_mute"].StateTopic != "mutedeck2mqtt/work" {
		t.Errorf("state topics: leave %q, mute %q", d.Components["work_leave"].StateTopic, d.Components["work_mute"].StateTopic)
	}
}

func TestConcurrentConfirmations(t *testing.T) {
	// Mute then video within the confirm window: finishing the first must neither
	// resume polls nor drop the second command's prediction
	mute := startConfirm(map[string]interface{}{"mute": "active"})
	video := startConfirm(map[string]interface{}{"video": "active"})
	toggle := startConfirm(nil)

	others := finishConfirm(mute)
	if !confirming() {
		t.Errorf("polls resumed while video and toggle are still confirming")
	}
	if others["video"] != "active" || others["mute"] != nil {
		t.Errorf("after mute: others = %v, want only video=active", others)
	}
	finishConfirm(mute) // harmless repeat, as from the deferred call
	finishConfirm(video)
	if !confirming() {
		t.Errorf("polls resumed while the toggle is still confirming")
	}
	if others := finishConfirm(toggle); len(others) != 0 || confirming() {
		t.Errorf("after all: others = %v, confirming = %v", others, confirming())
	}

	if watchedField("leave") != "call" || watchedField("mute") != "mute" {
		t.Errorf("watchedField: leave=%q mute=%q", watchedField("leave"), watchedField("mute"))
	}
}
