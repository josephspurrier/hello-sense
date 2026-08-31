# Voice sidecar: faster-whisper (STT) + piper (TTS) + ffmpeg, all CPU, models
# baked in so there is no per-request or first-request network dependency.
FROM python:3.11-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ffmpeg espeak-ng wget ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir -r /tmp/requirements.txt

# Piper voice (lessac medium): a natural en-US voice, ~63 MB. The model and its
# JSON config must sit side by side.
RUN mkdir -p /models \
    && wget -qO /models/voice.onnx \
       https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/lessac/medium/en_US-lessac-medium.onnx \
    && wget -qO /models/voice.onnx.json \
       https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/lessac/medium/en_US-lessac-medium.onnx.json

# Pre-download the Whisper weights into the image so the first request is fast
# and the container needs no network at runtime.
RUN python -c "from faster_whisper import WhisperModel; WhisperModel('base.en', device='cpu', compute_type='int8')"

COPY app.py /app/app.py
WORKDIR /app
EXPOSE 8090
CMD ["python", "app.py"]
