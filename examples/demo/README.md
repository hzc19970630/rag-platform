# Demo corpus

Two small Markdown documents for a first end-to-end run:

- `retrieval-handbook.md`: hybrid recall, evidence readiness, citations, retention.
- `product-faq.md`: supported formats, tenant isolation, embedding binding, refusal.

Upload either file in the web UI, wait for `EVIDENCE_READY`, then ask a question
that appears verbatim in the document. The local extractive chat adapter only cites
sentences that contain query terms, so exact phrasing produces a deterministic
answer.

Questions that work with the quickstart defaults:

- `How are keyword and vector results combined?`
- `What happens when the evidence is insufficient?`
- `Which document formats are supported?`

`./scripts/quickstart_check.sh` uses `retrieval-handbook.md` automatically.
