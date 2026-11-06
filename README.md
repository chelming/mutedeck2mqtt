# MuteDeck2MQTT

## Description

MuteDeck2MQTT is an application designed to work with [MuteDeck](https://mutedeck.com) to display call status in [Home Assistant](https://home-assistant.io). It leverages an MQTT server and MuteDeck's webhook functionality.

## Setup
### Sample Docker Run Command

```sh
docker run -d \
  --name mutedeck2mqtt \
  -e MQTT_HOST=<your_mqtt_host> \
  -e MQTT_USER=<your_mqtt_user> \
  -e MQTT_PASS=<your_mqtt_pass> \
  -e LOG_LEVEL=INFO \
  -e MQTT_PORT=1883 \
  -e HOME_ASSISTANT_DISCOVERY_TOPIC=homeassistant \
  -e MQTT_CLIENT_ID=mutedeck2mqtt \
  -e PORT=8080 \
  -p 8080:8080 \
  ghcr.io/chelming/mutedeck2mqtt
```

### Example Docker Compose YAML

```yaml
services:
  mutedeck2mqtt:
    image: ghcr.io/chelming/mutedeck2mqtt
    container_name: mutedeck2mqtt
    environment:
      - MQTT_HOST=<your_mqtt_host>
      - MQTT_USER=<your_mqtt_user>
      - MQTT_PASS=<your_mqtt_pass>
      - LOG_LEVEL=INFO
      - MQTT_PORT=1883
      - HOME_ASSISTANT_DISCOVERY_TOPIC=homeassistant
      - MQTT_CLIENT_ID=mutedeck2mqtt
      - PORT=8080
    ports:
      - "8080:8080"
```

### MuteDeck
To set it up, go to MuteDeck's settings, enable the webhook, and enter the URL for where you're running MuteDeck2MQTT. The URL should be formatted similarly to `http://localhost:8080/?topic=${name to appear in Home Assistant}`. You can also add an optional `prefix` parameter, which defaults to `mutedeck2mqtt`.

<img width="668" alt="Image showing the MuteDeck setting window with the Notifications tab selected. The Enable Webhook button is turned on and in the text box below http://mutedeck2mqtt.local:8080/?topic=MyComp is entered." src="https://github.com/user-attachments/assets/2bdd7434-fd81-4e16-b552-9a261d8ed729">


### Home Assistant
As long as MQTT is set up in Home Assistant, the device should automatically appear after it checks in for the first time using MQTT discovery. Discovery messages are retained. State is retained only for the device controlled through `MUTEDECK_API_URL`, which has availability to mark it stale; a webhook-only device's state is not retained, so it shows as unknown after a Home Assistant restart until the next webhook arrives. When Home Assistant restarts and broadcasts a Birth Message, mutedeck2mqtt will automatically rebroadcast the discovery messages.

### Controlling MuteDeck (optional)

Set `MUTEDECK_API_URL` to make one device controllable through MuteDeck's local API (the
API is documented by MuteDeck itself at `http://localhost:3491/swagger-ui` on the computer
running it). For that device:

| Entity | Type | Behavior |
|---|---|---|
| Microphone | switch | ON = microphone live. Turning it off mutes. Outside a call MuteDeck acts on the system microphone |
| Video | switch | ON = camera on. Unavailable outside a call, when MuteDeck reports video `disabled` |
| Leave meeting | button | Leaves the current meeting. Available only during a call |
| Call, Control, Recording, Screen sharing | sensors | As before |

How it stays responsive: after a command the bridge publishes the expected state at once.
MuteDeck's `/v1/status` lags a change by up to about 1.5 s, so the bridge then checks it
every 0.5 s for up to 3 s and publishes once MuteDeck agrees, or publishes whatever MuteDeck
reports when the window runs out. A toggle or Leave meeting has no expected state, so the
bridge reads `/v1/status` before acting and waits for the field to change instead. Polls
pause while any command is being confirmed, and confirming one command keeps the expected
state of any other still in flight (for example mute and then video in quick succession). It also
polls `/v1/status` every `MUTEDECK_POLL_INTERVAL` seconds, so the state stays right even
when a webhook is missed, and only publishes when something changed. The webhook still
works and merges into the same device when it uses the same `topic` and `prefix`.

Availability: the device is available only while both the bridge (MQTT last will on
`<prefix>/status`) and MuteDeck (`<prefix>/<topic>/availability`, set by the poll) are up.
Both are retained. They are only published when `MUTEDECK_API_URL` is set; webhook-only
devices have no availability.

Authentication: MuteDeck accepts calls without a token only from its own computer by
default. When the bridge runs elsewhere, create a token in MuteDeck under Settings >
Plugins & API (or pair through `POST /v1/pair`) and pass it as `MUTEDECK_API_TOKEN`.

Commands, if you want to drive it from something other than Home Assistant:

| Topic | Payload |
|---|---|
| `<prefix>/<topic>/mute/set` | `ON` (unmute), `OFF` (mute), `TOGGLE` |
| `<prefix>/<topic>/video/set` | `ON`, `OFF`, `TOGGLE` |
| `<prefix>/<topic>/leave` | `PRESS` |

## Environment Variables

### Required Variables

1. **MQTT_HOST**
   - **Description**: The hostname or IP address of the MQTT broker.
   - **Required**: Yes
   - **Default Value**: None

2. **MQTT_USER**
   - **Description**: The username for authenticating with the MQTT broker.
   - **Required**: Yes
   - **Default Value**: None

3. **MQTT_PASS**
   - **Description**: The password for authenticating with the MQTT broker.
   - **Required**: Yes
   - **Default Value**: None

### Optional Variables

1. **LOG_LEVEL**
   - **Description**: The log level for the application (DEBUG, INFO, WARN, ERROR).
   - **Required**: No
   - **Default Value**: INFO

2. **MQTT_PORT**
   - **Description**: The port number for the MQTT broker.
   - **Required**: No
   - **Default Value**: 1883

3. **HOME_ASSISTANT_DISCOVERY_TOPIC**
   - **Description**: The discovery prefix for Home Assistant.
   - **Required**: No
   - **Default Value**: homeassistant

4. **MQTT_CLIENT_ID**
   - **Description**: The client identifier for the MQTT connection.
   - **Required**: No
   - **Default Value**: mutedeck2mqtt

5. **PORT**
   - **Description**: The port number for the HTTP server.
   - **Required**: No
   - **Default Value**: 8080

6. **MUTEDECK_API_URL**
   - **Description**: Base URL of MuteDeck's API, e.g. `http://192.168.1.20:3491`. Enables control (see above). Unset keeps the bridge webhook-only.
   - **Required**: No
   - **Default Value**: None

7. **MUTEDECK_API_TOKEN**
   - **Description**: Bearer token for the MuteDeck API. Needed when the bridge does not run on the same computer as MuteDeck.
   - **Required**: No
   - **Default Value**: None

8. **MUTEDECK_TOPIC**
   - **Description**: Topic of the controllable device, the same value as `topic` in the webhook URL. It also names the device in Home Assistant.
   - **Required**: No
   - **Default Value**: mutedeck

9. **MUTEDECK_PREFIX**
   - **Description**: Prefix of the controllable device, the same value as `prefix` in the webhook URL. Also the prefix of the bridge's `<prefix>/status` topic.
   - **Required**: No
   - **Default Value**: mutedeck2mqtt

10. **MUTEDECK_POLL_INTERVAL**
    - **Description**: Seconds between `/v1/status` polls. `0` disables polling (the device is then always reported available).
    - **Required**: No
    - **Default Value**: 5

## How the App Functions

MuteDeck2MQTT operates by setting up an HTTP server that listens for incoming webhook requests from MuteDeck. When a request is received, the app parses the JSON data, validates it, and publishes it to the specified MQTT topic. The app also sends discovery messages to Home Assistant to ensure that the devices are recognized and properly configured.

The app uses environment variables to configure its behavior, including the MQTT broker details, log level, and server port. It logs messages based on the specified log level, helping you manage log verbosity and troubleshoot issues.

By integrating MuteDeck2MQTT with MuteDeck and Home Assistant, you can easily monitor and display call status information in your smart home setup.
