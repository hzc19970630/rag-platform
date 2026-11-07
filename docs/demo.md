# Demo script

A 15-minute walkthrough for a local or recorded demo. It uses only the quickstart
stack and the sample corpus in
[examples/demo](https://github.com/hzc19970630/rag-platform/tree/main/examples/demo).

## Prepare

```bash
cp .env.example .env
./scripts/quickstart.sh
```

Open <http://localhost:5173> and click **Continue** on the development login panel.
The quickstart uses deterministic local models so the demo does not depend on a
provider or network.

## 1. Create a knowledge base

1. On **Knowledge bases**, choose **New**.
2. Name it `Demo Knowledge Base` and add a short description.
3. Open it. Point out the document tab, member tab, and settings entry.

Talking point: every knowledge base is a tenant-scoped resource; members and roles
are enforced by the API, not only hidden in the UI.

## 2. Ingest a document

1. Choose **Upload** and select `examples/demo/retrieval-handbook.md`.
2. Show the document row and the processing state.
3. Wait for `EVIDENCE_READY`, then open the document detail to show parsed content,
   chunks, versions, and the original preview.
4. Upload `examples/demo/product-faq.md` as a second document.

Talking point: the upload response is a pipeline handle. Parsing, chunking,
embedding, and indexing run asynchronously with retries and a durable stage state.

## 3. Ask a grounded question

1. Open **Chat** and create a conversation for the knowledge base.
2. Ask: `How does hybrid recall combine keyword and vector results?`
3. Show the streamed states, the answer, and the citation list.
4. Open a citation and show the source chunk and document.
5. Ask a question the corpus cannot answer, for example:
   `What is the sedimentary composition of the cretaceous layers?`
6. Show the refusal with `insufficient_evidence` and no citations.

Talking point: the model cannot cite evidence that was not retrieved and authorized.
When evidence is insufficient, the platform refuses instead of inventing an answer.

## 4. Show governance

1. Open **Knowledge base → Members** and show owner/editor/viewer roles.
2. Open **Settings** and show retrieval overrides and indexing capabilities.
3. Open **Administration** (tenant owner/admin) and show the tenant overview,
   quota overrides, member roles, and the audit filters.
4. Export the audit CSV and show that formula-like values are escaped.

Talking point: quota changes, membership changes, and audit are tenant-scoped and
recorded; the audit export is safe to open in a spreadsheet.

## 5. Show operations

1. Open **Operations** and show queue depth, pipeline stages, failure rate, dead
   letters, and outbox backlog.
2. Trigger a reconcile or inspect a dead letter without retrying it in a live demo.
3. Show `GET /healthz`, `GET /readyz`, and the build version if you are using a
   release image.

Talking point: the system exposes the state needed to operate ingestion, not only
request logs.

## 6. Optional: verify the API path

The same flow is available without the UI:

```bash
./scripts/quickstart_check.sh
```

This creates a temporary knowledge base, uploads the demo document, waits for
`EVIDENCE_READY`, checks hybrid search, streams a cited answer, and cleans up.

## Recording checklist

- Use a clean stack and a fixed browser window (1280×800 or 1440×900).
- Wait for `EVIDENCE_READY` before recording the question; otherwise the demo shows
  processing instead of citations.
- Keep the audit and operations pages read-only during the recording.
- Do not show `.env`, terminal history, API keys, or personal data.
- Re-record after UI changes; screenshots and videos age quickly.

## What not to claim

- Do not claim OCR, image understanding, or Kubernetes support.
- Do not claim that the local deterministic model is a production LLM.
- Do not compare benchmark numbers with other products without running the same
  corpus and configuration.
