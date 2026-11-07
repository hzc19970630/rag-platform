# Retrieval Handbook

## Hybrid recall

The evidence index answers questions with hybrid recall: BM25 keyword scoring runs in
parallel with vector cosine similarity, and the two ranked lists are merged with
reciprocal rank fusion. Reciprocal rank fusion uses a rank constant to keep scores
comparable across channels.

## Evidence readiness

Only a document version in the EVIDENCE_READY state can be retrieved. A replaced or
failed version is retired from retrieval, and its chunks are no longer served.

## Citations

Every delivered answer must carry citations that point to chunks in the authorized
evidence set. If the evidence is insufficient, the platform refuses to answer
instead of inventing facts.

## Retention

Soft-deleted documents remain restorable for the configured retention window. After
that window a worker purges the versions, chunks, and stored objects.
