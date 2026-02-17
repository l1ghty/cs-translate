# CS2 In-Game Translate & Voice Transcription

This tool translates Counter-Strike 2 chat messages in real-time and provides **local** voice chat transcription using OpenAI Whisper (running locally on your machine).

## Prerequisites

1.  **Go 1.24+**: Ensure Go is installed.
2.  **FFmpeg**: Required for audio capture.
    - Linux: `sudo apt install ffmpeg`
3.  **Python 3.9+**: Required for running Whisper.
    - It is recommended to use a virtual environment.
    - `sudo apt install python3-venv` (if not already installed)
4.  **Google Cloud Translate API Key**: Required for text translation.
    - (API Key for Voice Transcription is NO LONGER needed as it runs locally).

## Installation

1.  Clone/Download the repository.
2.  Setup Python environment for Whisper:
    ```bash
    python3 -m venv venv
    ./venv/bin/pip install openai-whisper
    ```
    *Note: This might download PyTorch (~1GB) and the Whisper model on first run.*

3.  Build the Go application:
    ```bash
    go mod tidy
    go build -o cs-translate
    ```

## Usage

Run the tool. You will be prompted to enable Voice Transcription.

```bash
export GOOGLE_API_KEY="your_google_key"
./cs-translate
```

Or with flags:

```bash
./cs-translate -apikey "your_google_key" -voice
```

### Audio Device Selection

By default, the tool tries to detect the monitor of your default audio output (to capture game audio including voice chat). If detection fails or you want to use a specific device:

1.  List available PulseAudio sources:
    ```bash
    pactl list short sources
    ```
2.  Find the source name ending in `.monitor`.
3.  Run with `-audiodevice`:
    ```bash
    ./cs-translate -audiodevice "alsa_output.pci-0000_00_1f.3.analog-stereo.monitor"
    ```

## Troubleshooting

- **"transcriber.py not found"**: Ensure you run the `cs-translate` binary from the project root directory where `transcriber.py` resides.
- **"openai-whisper not found"**: Ensure you installed the python dependencies in the `venv` directory or your system python.
- **Model Loading**: The first time you enable voice, it will download the "base" Whisper model. This might take a minute.
