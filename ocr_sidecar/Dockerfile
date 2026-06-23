FROM python:3.11-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    OMP_NUM_THREADS=1 \
    MKL_NUM_THREADS=1

WORKDIR /app

COPY requirements.txt main.py entrypoint.sh ./

RUN pip install --no-cache-dir --upgrade pip && \
    pip install --no-cache-dir -r requirements.txt && \
    chmod +x entrypoint.sh

EXPOSE 8000

# entrypoint.sh launches one uvicorn worker per CPU core for parallel OCR.
CMD ["./entrypoint.sh"]
