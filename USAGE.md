# CS2 In-Game Translate & Voice Transcription
    
This tool translates Counter-Strike 2 chat messages in real-time and provides **local** voice chat transcription using OpenAI Whisper (running locally on your machine).
    
## Prerequisites
    
1.  **Go 1.24+**: Ensure Go is installed (only if building from source).
2.  **FFmpeg**: Required on all platforms for audio capture.
    - **Linux**: `sudo apt install ffmpeg`
    - **Windows**: Download `ffmpeg` binaries (gyan.dev or equivalent), extract, and add the `bin` folder to your System PATH environment variable.
3.  **Python 3.9+**: Required for running Whisper.
    - **Linux**: `sudo apt install python3-venv`
    - **Windows**: Install Python from python.org. Ensure you check "Add Python to PATH" during installation.
    
## Installation
    
1.  **Setup Python environment**:
    - **Linux**:
      ```bash
      python3 -m venv venv
      ./venv/bin/pip install openai-whisper
      ```
    - **Windows**:
      Open Command Prompt or PowerShell in the project folder:
      ```cmd
      python -m venv venv
      .\venv\Scripts\pip install openai-whisper
      ```
      *Note: This might download PyTorch (~1GB) and the Whisper model on first run.*
    
2.  **Build or Download Binary**:
    - **Linux**: `go build -o cs-translate`
    - **Windows**: `go build -o cs-translate.exe` (or use the provided pre-compiled exe).
    
    *Ensure `transcriber.py` is in the same folder as the executable.*
    
## Usage
    
### Linux
    
```bash
export GOOGLE_API_KEY="your_google_key"
./cs-translate -voice
```
    
### Windows
    
1.  Set your API Key environment variable (optional, or pass via flag).
2.  Run from Command Prompt / PowerShell:
    ```cmd
    cs-translate.exe -apikey "your_google_key" -voice -audiodevice "Microphone (Realtek High Definition Audio)"
    ```
    
**Important for Windows Audio**:
There is no "default" audio capture on Windows via FFmpeg like on Linux. You **MUST** specify the input device name.
    
1.  **List Devices**:
    Run this command in terminal to see available audio devices:
    ```cmd
    ffmpeg -list_devices true -f dshow -i dummy
    ```
    Look for entries under "DirectShow audio devices".
    
2.  **Run with Device Name**:
    If your device is named "Microphone (Realtek(R) Audio)", run:
    ```cmd
    cs-translate.exe -voice -audiodevice "Microphone (Realtek(R) Audio)"
    ```
    To capture **Game Audio** (loopback), you typically need "Stereo Mix" enabled in Windows Sound settings, or a virtual cable. If enabled, use:
    ```cmd
    cs-translate.exe -voice -audiodevice "Stereo Mix (Realtek(R) Audio)"
    ```
    
## Troubleshooting
    
- **"transcriber.py not found"**: Ensure `transcriber.py` is in the working directory.
- **FFmpeg errors**: Ensure `ffmpeg` is in your PATH. Open a new terminal and type `ffmpeg -version` to check.
- **Audio Device Error**: Double-check the name with the `ffmpeg -list_devices` command. It must match exactly.
