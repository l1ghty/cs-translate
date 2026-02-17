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
    - If `python` is missing, it will offer to install it.
    - It will automatically set up the Python virtual environment and dependencies.

### Windows
1.  **Run**:
    ```cmd
    cs-translate.exe -apikey "your_google_key" -voice -audiodevice "Stereo Mix (Realtek(R) Audio)"
    ```
2.  **Auto-Setup**:
    - If `python` is missing, it will offer to install it (Python 3.11 via Winget).
    - It will automatically set up the Python virtual environment and dependencies.

## Manual Dependencies (if auto-setup fails)

- **Python 3.9+**: https://python.org

## Windows Audio Note

Audio capture uses native `malgo` loopback. You can run with the default output device:

1.  **Run**: `cs-translate.exe -voice`
2.  **Optional Device Selection**: `cs-translate.exe -voice -audiodevice "Speakers"`
    
## Troubleshooting
    
- **"transcriber.py not found"**: Ensure `transcriber.py` is in the working directory.
