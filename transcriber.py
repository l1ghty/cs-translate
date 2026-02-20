
import sys
import os
import signal
import warnings

# Suppress unimportant warnings
warnings.filterwarnings("ignore")

def handle_sigterm(*args):
    sys.exit(0)

signal.signal(signal.SIGTERM, handle_sigterm)

def main():
    try:
        import whisper
    except ImportError:
        print("Error: 'openai-whisper' python package not found. Please install it: pip install openai-whisper", file=sys.stderr)
        sys.exit(1)

    # Print to stderr so main program can differentiate logs from data
    whisper_model = os.environ.get("WHISPER_MODEL", "base")
    print(f"Loading Whisper model '{whisper_model}'...", file=sys.stderr)

    try:
        model = whisper.load_model(whisper_model)
        print("Whisper model loaded.", file=sys.stderr)
    except Exception as e:
        print(f"Failed to load model: {e}", file=sys.stderr)
        sys.exit(1)

    print("READY", flush=True)

    for line in sys.stdin:
        path = line.strip()
        if not path:
            continue
            
        try:
            # Check if file exists
            if not os.path.exists(path):
                print(f"File not found: {path}", file=sys.stderr)
                continue

            result = model.transcribe(path)
            text = result["text"].strip().replace("\n", " ")
            print(text, flush=True)
            
            # Optional: remove file after processing? Go code does it.
        except Exception as e:
            print(f"Error processing {path}: {e}", file=sys.stderr)
            print("", flush=True) # Send empty line to signal done/error

if __name__ == "__main__":
    # Force UTF-8 for Windows console
    if sys.platform == "win32":
        sys.stdout.reconfigure(encoding='utf-8')
        sys.stderr.reconfigure(encoding='utf-8')

    try:
        main()
    except KeyboardInterrupt:
        sys.exit(0)
