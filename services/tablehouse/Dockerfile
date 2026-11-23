FROM python:3.12-slim@sha256:4840d514c583a4d3d8cad3940e68dee6f0f6f3635c12fcd0a3d74c21a5b93a54

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
LABEL org.opencontainers.image.title="rag-platform-tablehouse" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.source="https://github.com/hzc19970630/rag-platform"

RUN useradd --create-home --uid 10002 tablehouse
WORKDIR /app
COPY services/tablehouse/main.py services/tablehouse/requirements.txt ./
ARG PIP_INDEX_URL=https://pypi.org/simple
RUN pip install --no-cache-dir --index-url "${PIP_INDEX_URL}" --timeout 60 --retries 3 -r requirements.txt
USER tablehouse
EXPOSE 8088
CMD ["python", "main.py"]