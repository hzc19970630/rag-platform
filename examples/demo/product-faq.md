# Product FAQ

## What document formats are supported?

The parser accepts digital PDF files with an extractable text layer, DOCX, XLSX,
PPTX, Markdown, TXT, and HTML. Scanned PDFs, images, and legacy OLE files are
rejected with a stable error code; OCR is not part of the current release.

## How is tenant isolation enforced?

Every retrieval query carries the tenant and knowledge-base scope, and a second
authorization check runs before delivery. Deployments can additionally enable
PostgreSQL row-level security for defense in depth.

## Can a knowledge base use more than one embedding model?

No. A knowledge base is bound to one embedding model and dimension. Changing it is a
managed re-index operation: build the new index, re-embed existing chunks, verify,
then switch traffic.

## What happens when the evidence is insufficient?

The answer is refused with an `insufficient_evidence` code and no citations. The
model is not allowed to fill gaps with general knowledge.
