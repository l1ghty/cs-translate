# CS2 In-Game Translate & Voice Transcription
    
This tool translates Counter-Strike 2 chat messages in real-time and provides **local** voice chat transcription using OpenAI Whisper.
    
## Prerequisites & Installation

The tool includes **automatic dependency installation**!

### Linux
1.  **Run**:
    ```bash
    export GOOGLE_API_KEY="your_google_key"
    ./cs-translate -voice
    ```
2.  **Auto-Setup**:
    - If `ffmpeg` is missing, it will offer to install it via your package manager (`apt`, `dnf`, `pacman`).
    - If `python` is missing, it will offer to install it.
    - It will automatically set up the Python virtual environment and dependencies.

### Windows
1.  **Run**:
    ```cmd
    cs-translate.exe -apikey "your_google_key" -voice -audiodevice "Stereo Mix (Realtek(R) Audio)"
    ```
2.  **Auto-Setup**:
    - If `ffmpeg` is missing, it will offer to install it via **Winget**, **Chocolatey**, or **Scoop**.
    - If `python` is missing, it will offer to install it (Python 3.11 via Winget).
    - It will automatically set up the Python virtual environment and dependencies.

## Manual Dependencies (if auto-setup fails)

- **FFmpeg**: https://gyan.dev/ffmpeg/builds/ (Windows) or `sudo apt install ffmpeg` (Linux)
- **Python 3.9+**: https://python.org

## Windows Audio Note
    
On Windows, you **MUST** specify the input device name if you want to capture game audio (use "Stereo Mix" or Virtual Cable).
    
1.  **List Devices**: `ffmpeg -list_devices true -f dshow -i dummy`
2.  **Run**: `cs-translate.exe -voice -audiodevice "Your Device Name"`
    
## Troubleshooting
    
- **"transcriber.py not found"**: Ensure `transcriber.py` is in the working directory.
