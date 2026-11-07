FROM python:3.12-slim@sha256:4840d514c583a4d3d8cad3940e68dee6f0f6f3635c12fcd0a3d74c21a5b93a54
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
LABEL org.opencontainers.image.title="rag-platform-docreader" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.source="https://github.com/hzc19970630/rag-platform"

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
