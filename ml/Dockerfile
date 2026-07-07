FROM python:3.12-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    tesseract-ocr tesseract-ocr-spa \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY pyproject.toml ./
RUN pip install --no-cache-dir -e .
COPY . .
EXPOSE 8000
CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8000"]
