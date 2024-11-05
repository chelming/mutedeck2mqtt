# MuteDeck2MQTT

## Description

MuteDeck2MQTT is an application designed to work with [MuteDeck](https://mutedeck.com) to display call status in Home Assistant. It leverages an MQTT server and MuteDeck's webhook functionality. To set it up, go to MuteDeck's settings, enable the webhook, and enter the URL for the MuteDeck2MQTT app. The URL should be `http://localhost:8080/?topic=${name to appear in Home Assistant}`. Note that the URL may change if you're using a reverse proxy. You can also add an optional `prefix` parameter, which defaults to `mutedeck2mqtt`.

## Sample Docker Run Command

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

## Example Docker Compose YAML

```yaml
version: '3.8'

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

## How the App Functions

MuteDeck2MQTT operates by setting up an HTTP server that listens for incoming webhook requests from MuteDeck. When a request is received, the app parses the JSON data, validates it, and publishes it to the specified MQTT topic. The app also sends discovery messages to Home Assistant to ensure that the devices are recognized and properly configured.

The app uses environment variables to configure its behavior, including the MQTT broker details, log level, and server port. It logs messages based on the specified log level, helping you manage log verbosity and troubleshoot issues.

By integrating MuteDeck2MQTT with MuteDeck and Home Assistant, you can easily monitor and display call status information in your smart home setup.