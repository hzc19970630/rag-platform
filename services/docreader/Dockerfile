FROM python:3.12-slim

RUN useradd --create-home --uid 10001 docreader
WORKDIR /app
COPY services/docreader/main.py services/docreader/requirements.txt ./
COPY contracts/proto/docreader/v1/docreader.proto /tmp/docreader.proto
RUN pip install --no-cache-dir -r requirements.txt \
    && mkdir -p /app/generated \
    && python -m grpc_tools.protoc -I/tmp --python_out=/app/generated --grpc_python_out=/app/generated /tmp/docreader.proto \
    && touch /app/generated/__init__.py
USER docreader
EXPOSE 50051 8080
CMD ["python", "main.py"]
