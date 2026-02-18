FROM ubuntu:22.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    ffmpeg \
    libsndfile1 \
    python3 \
    python3-pip \
    docker.io \
    zstd \
    && rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir openai-whisper

RUN curl -fsSL https://ollama.com/install.sh | sh

WORKDIR /app

COPY transcriber.py /app/transcriber.py

ENV WHISPER_MODEL=turbo
ENV OLLAMA_HOST=0.0.0.0
ENV OLLAMA_MODELS=/data/ollama
ENV XDG_CACHE_HOME=/data/cache

CMD ["sh", "-c", "mkdir -p /data/ollama /data/cache && ollama serve & while true; do sleep 30; done"]