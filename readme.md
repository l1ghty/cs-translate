# CS2 In-Game Translate & Voice Transcription

This tool translates Counter-Strike 2 chat messages in real-time and provides **local** voice chat transcription using OpenAI Whisper and Ollama for translation.

## Prerequisites

### Windows additional dependencies
#### Windows Audio Device Selection:
- Requires: https://github.com/rdp/screen-capture-recorder-to-video-windows-free
- No device selection needed - app uses virtual-audio-capturer by default

### Automatic Setup
The tool includes automatic dependency installation. If dependencies are missing, it will offer to set them up.

#### Dependencies Will be installed automatically if missing
- **Ollama**: Install from https://ollama.ai and ensure it's running
- **Python 3.9+**: For Whisper transcription
- **FFmpeg**: Required for audio capture

## Usage

### Quick Start

**Linux/macOS:**
```bash
./cs-translate --lang Spanish # Defaults to English if no language specified
```

**Windows:**
```cmd
cs-translate.exe -lang Spanish # Defaults to English if no language specified
```

### Command Line Options

| Flag | Description | Default |
|------|-------------|---------|
| `-voice` | Enable voice transcription (local Whisper) |
| `-log` | Path to CS2 console log file | Auto-detect |
| `-model` | Ollama model for translation | `hf.co/blackcloud1199/qwen-translation-vi` |
| `-lang` | Target language for translation | `English` |
| `-audiodevice` | Audio device for voice capture | Auto-detect |
| `-list-audio-devices` | List available audio devices and exit | - |

### Examples

**With custom Ollama model:**
```bash
./cs-translate -model llama3 -lang Spanish -voice
```

**Specify log file manually:**
```bash
./cs-translate -log /path/to/console.log
```

## Features

- **Chat Translation**: Translates in-game chat messages to your target language using local Ollama LLM
- **Voice Transcription**: Captures and transcribes voice chat using Whisper (local, privacy-friendly)
- **Auto Log Detection**: Automatically finds the CS2 console.log file
- **Voice Context**: Provides last 10 seconds of transcription context for better translation accuracy

