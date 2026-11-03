#!/usr/bin/env bash
# M3 acceptance: authorized RAG, citations and streaming delivery.
#
# Covers the M3 acceptance gates: preview/chunk endpoints, hybrid recall with
# scope filtering, the SSE event contract and resume, citation validation,
# refusal behaviour and the retrieval parameter override chain.
set -euo pipefail

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

base_url="http://localhost:${API_PORT:-8082}"
compose=(docker compose --env-file .env -f deploy/docker-compose/docker-compose.yml)
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

dev_headers=(-H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant')
other_headers=(-H 'X-Dev-User-ID: other-user' -H 'X-Dev-Tenant-ID: other-tenant')

request() {
  local method="$1" path="$2" expected="$3" output="$4"
  shift 4
  local code
  code="$(curl -sS -o "$output" -w '%{http_code}' -X "$method" "$base_url$path" "$@")"
  if [[ "$code" != "$expected" ]]; then
    printf 'expected %s %s, got %s: ' "$method" "$path" "$code" >&2
    cat "$output" >&2
    exit 1
  fi
}

json() {
  python3 -c "import json,sys; print(json.load(open(sys.argv[1]))$1)" "$2"
}

json_len() {
  python3 -c "import json,sys; print(len(json.load(open(sys.argv[1]))$1))" "$2"
}

psql_value() {
  "${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "$1"
}

curl -fsS "$base_url/readyz" | grep -q '"status":"ok"'

# ---------------------------------------------------------------------------
# Fixture: one document with distinctive English and Chinese content.
# ---------------------------------------------------------------------------
printf '# Retrieval Handbook\n\n## Hybrid recall\nThe evidence index answers questions with hybrid recall: BM25 keyword scoring runs in parallel with vector cosine similarity, and the two ranked lists are merged with reciprocal rank fusion.\n\n## Thresholds\nThe vector threshold is a cosine similarity floor between 0 and 1.\n\n## Evidence readiness\n只有达到 EVIDENCE_READY 状态的文档版本可以被检索。\n' > "$tmp_dir/handbook.md"
upload_key="m3-acceptance-$(date +%s)"
request POST /api/v1/knowledge-bases/kb-dev/documents 202 "$tmp_dir/upload.json" \
  "${dev_headers[@]}" -H "Idempotency-Key: $upload_key" -F "file=@$tmp_dir/handbook.md;type=text/markdown"
document_id="$(json '["document"]["id"]' "$tmp_dir/upload.json")"
version_id="$(json '["version"]["id"]' "$tmp_dir/upload.json")"

for _ in $(seq 1 40); do
  request GET "/api/v1/documents/$document_id" 200 "$tmp_dir/status.json" "${dev_headers[@]}"
  pipeline_status="$(json '["pipeline"]["status"]' "$tmp_dir/status.json")"
  [[ "$pipeline_status" == "EVIDENCE_READY" ]] && break
  if [[ "$pipeline_status" == "FAILED" ]]; then
    cat "$tmp_dir/status.json" >&2
    exit 1
  fi
  sleep 1
done
[[ "$pipeline_status" == "EVIDENCE_READY" ]]

# ---------------------------------------------------------------------------
# Document detail: processing status, parsed content, chunk paging, preview.
# ---------------------------------------------------------------------------
request GET "/api/v1/documents/$document_id/status" 200 "$tmp_dir/detail.json" "${dev_headers[@]}"
[[ "$(json '["contentReady"]' "$tmp_dir/detail.json")" == "True" ]]
[[ "$(json '["previewable"]' "$tmp_dir/detail.json")" == "True" ]]
[[ "$(json_len '["stages"]' "$tmp_dir/detail.json")" == "5" ]]
if grep -q 'sourceObjectKey\|normalizedKey\|tenants/dev-tenant' "$tmp_dir/detail.json" "$tmp_dir/status.json"; then
  echo "a document read response exposed an object storage key" >&2
  exit 1
fi

request GET "/api/v1/documents/$document_id/content" 200 "$tmp_dir/content.json" "${dev_headers[@]}"
[[ "$(json '["schemaVersion"]' "$tmp_dir/content.json")" == "DocumentPreviewV1" ]]
[[ "$(json_len '["blocks"]' "$tmp_dir/content.json")" -gt 0 ]]
grep -q 'reciprocal rank fusion' "$tmp_dir/content.json"

request GET "/api/v1/documents/$document_id/chunks?limit=1" 200 "$tmp_dir/chunks1.json" "${dev_headers[@]}"
[[ "$(json '["schemaVersion"]' "$tmp_dir/chunks1.json")" == "ChunkPageV1" ]]
[[ "$(json_len '["items"]' "$tmp_dir/chunks1.json")" == "1" ]]
[[ "$(json '["hasMore"]' "$tmp_dir/chunks1.json")" == "True" ]]
first_cursor="$(json '["nextCursor"]' "$tmp_dir/chunks1.json")"
[[ "$first_cursor" -gt 0 ]]
request GET "/api/v1/documents/$document_id/chunks?limit=1&cursor=$first_cursor" 200 "$tmp_dir/chunks2.json" "${dev_headers[@]}"
first_chunk_id="$(json '["items"][0]["chunkId"]' "$tmp_dir/chunks1.json")"
second_chunk_id="$(json '["items"][0]["chunkId"]' "$tmp_dir/chunks2.json")"
[[ "$first_chunk_id" != "$second_chunk_id" ]]
[[ "$(json '["items"][0]["documentVersionId"]' "$tmp_dir/chunks1.json")" == "$version_id" ]]

request GET "/api/v1/chunks/$first_chunk_id" 200 "$tmp_dir/chunk.json" "${dev_headers[@]}"
[[ "$(json '["chunkId"]' "$tmp_dir/chunk.json")" == "$first_chunk_id" ]]
[[ "$(json '["knowledgeBaseId"]' "$tmp_dir/chunk.json")" == "kb-dev" ]]

preview_code="$(curl -sS -D "$tmp_dir/preview.headers" -o "$tmp_dir/preview.body" -w '%{http_code}' "$base_url/api/v1/documents/$document_id/preview" "${dev_headers[@]}")"
[[ "$preview_code" == "200" ]]
grep -i '^Content-Disposition: inline' "$tmp_dir/preview.headers" >/dev/null
grep -i '^X-Content-Type-Options: nosniff' "$tmp_dir/preview.headers" >/dev/null
grep -q 'reciprocal rank fusion' "$tmp_dir/preview.body"

# ---------------------------------------------------------------------------
# Retrieval-only search: hybrid recall filtered by the authorization scope.
# ---------------------------------------------------------------------------
request POST /api/v1/search 200 "$tmp_dir/search.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' \
  -d '{"query":"how are keyword and vector results combined","knowledgeBaseId":"kb-dev"}'
[[ "$(json '["schemaVersion"]' "$tmp_dir/search.json")" == "EvidenceSetV1" ]]
[[ "$(json_len '["items"]' "$tmp_dir/search.json")" -gt 0 ]]
[[ "$(json '["plan"]["intent"]' "$tmp_dir/search.json")" == "fact" ]]
[[ "$(json '["plan"]["mode"]' "$tmp_dir/search.json")" == "evidence" ]]
[[ "$(json '["plan"]["vectorEnabled"]' "$tmp_dir/search.json")" == "True" ]]
[[ "$(json '["plan"]["keywordEnabled"]' "$tmp_dir/search.json")" == "True" ]]
[[ "$(json '["items"][0]["knowledgeBaseId"]' "$tmp_dir/search.json")" == "kb-dev" ]]
# RRF fusion records which channel produced each candidate.
grep -Eq '"vectorRank"|"keywordRank"' "$tmp_dir/search.json"

# A question with no lexical or semantic support must return no evidence.
#
# The offline local embedder produces hash vectors without any semantic notion
# of relevance, so unrelated chunks can clear a low cosine floor by chance. The
# refusal path is therefore exercised with a request-level override that
# requires an exact vector match, which the hash embedder only produces for
# identical text. Keyword recall already finds nothing for this question.
no_evidence_override='{\"vector_threshold\":1.0}'
request POST /api/v1/search 200 "$tmp_dir/search-empty.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' \
  -d "{\"query\":\"cretaceous period dinosaur migration fossils\",\"knowledgeBaseId\":\"kb-dev\",\"sessionOverride\":\"$no_evidence_override\"}"
[[ "$(json_len '["items"]' "$tmp_dir/search-empty.json")" == "0" ]]

# ---------------------------------------------------------------------------
# Streaming answer: event order, citation delivery and persistence.
# ---------------------------------------------------------------------------
request POST /api/v1/sessions 201 "$tmp_dir/session.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"knowledge_base_id":"kb-dev"}'
conversation_id="$(json '["id"]' "$tmp_dir/session.json")"

request_id="m3-stream-$(date +%s)"
ask_code="$(curl -sS -N -o "$tmp_dir/stream.txt" -w '%{http_code}' -X POST "$base_url/api/v1/sessions/$conversation_id/messages" \
  "${dev_headers[@]}" -H 'Content-Type: application/json' -H 'Accept: text/event-stream' \
  -d "{\"query\":\"how does hybrid recall combine keyword and vector results\",\"requestId\":\"$request_id\"}")"
[[ "$ask_code" == "200" ]]

for event_type in status references answer complete; do
  grep -q "^event: $event_type$" "$tmp_dir/stream.txt"
done
python3 - "$tmp_dir/stream.txt" <<'PY'
import json
import re
import sys

raw = open(sys.argv[1], encoding="utf-8").read()
blocks = [block for block in raw.split("\n\n") if block.strip()]
sequences = []
event_types = []
states = []
for block in blocks:
    event_type = re.search(r"^event: (.+)$", block, re.M).group(1)
    payload = json.loads(re.search(r"^data: (.+)$", block, re.M).group(1))
    event_types.append(event_type)
    sequences.append(payload["sequence"])
    assert payload["event_id"] == f'{payload["request_id"]}:{payload["sequence"]}', payload
    assert payload["type"] == event_type, payload
    if event_type == "status":
        states.append(payload["data"]["state"])
assert sequences == sorted(sequences), sequences
assert len(set(sequences)) == len(sequences), sequences
assert sequences == list(range(1, len(sequences) + 1)), sequences
assert event_types[-1] == "complete", event_types
assert "references" in event_types and event_types.index("references") < event_types.index("answer")

# The online state machine must be reported in order, and the delivery states
# must all be observed for an answered question.
expected_states = ["RECEIVED", "AUTHORIZED", "PLANNED", "RETRIEVING", "GROUNDED", "GENERATING", "CITATION_VALIDATING", "DELIVERING"]
assert states == expected_states, states

answer = next(json.loads(re.search(r"^data: (.+)$", block, re.M).group(1))
              for block, event_type in zip(blocks, event_types) if event_type == "answer")
assert answer["data"]["status"] == "COMPLETED", answer
assert answer["data"]["citations"], "the answer must carry citations"
assert answer["data"]["validation"]["valid"] is True, answer["data"]["validation"]
assert answer["data"]["evidenceChunkIds"], "the answer must report its evidence set"
evidence_ids = set(answer["data"]["evidenceChunkIds"])
assert re.search(r"\[\d+\]", answer["data"]["content"]), answer["data"]["content"]
for citation in answer["data"]["citations"]:
    assert citation["chunkId"], citation
    assert citation["documentVersionId"], citation
    # Every delivered citation must come from this run's authorized evidence.
    assert citation["chunkId"] in evidence_ids, citation
PY

request GET "/api/v1/sessions/$conversation_id/messages" 200 "$tmp_dir/messages.json" "${dev_headers[@]}"
[[ "$(json_len '["items"]' "$tmp_dir/messages.json")" -ge 2 ]]
[[ "$(json '["items"][-1]["message"]["role"]' "$tmp_dir/messages.json")" == "assistant" ]]
[[ "$(json '["items"][-1]["message"]["status"]' "$tmp_dir/messages.json")" == "COMPLETED" ]]
[[ "$(json_len '["items"][-1]["citations"]' "$tmp_dir/messages.json")" -gt 0 ]]

message_id="$(json '["items"][-1]["message"]["id"]' "$tmp_dir/messages.json")"
citation_rows="$(psql_value "SELECT count(*) FROM message_citations WHERE message_id = '$message_id';")"
[[ "$citation_rows" -gt 0 ]]
# Every persisted citation must point at a chunk of the current version.
[[ "$(psql_value "SELECT count(*) FROM message_citations c JOIN chunks k ON k.id = c.chunk_id WHERE c.message_id = '$message_id' AND k.document_version_id <> '$version_id';")" == "0" ]]
# The answer provenance is recorded for reproduction.
[[ "$(psql_value "SELECT count(*) FROM messages WHERE id = '$message_id' AND query_plan IS NOT NULL AND evidence_set IS NOT NULL AND citation_validation IS NOT NULL;")" == "1" ]]

# ---------------------------------------------------------------------------
# Resume: replaying the persisted stream from an offset.
# ---------------------------------------------------------------------------
request GET "/api/v1/requests/$request_id/events?after=3" 200 "$tmp_dir/resume.txt" "${dev_headers[@]}"
first_sequence="$(grep -o '"sequence":[0-9]*' "$tmp_dir/resume.txt" | head -1 | grep -o '[0-9]*')"
[[ "$first_sequence" == "4" ]]
grep -q '^event: complete$' "$tmp_dir/resume.txt"
resumed_total="$(grep -c '^event: ' "$tmp_dir/resume.txt")"
stream_total="$(grep -c '^event: ' "$tmp_dir/stream.txt")"
[[ "$resumed_total" == "$((stream_total - 3))" ]]

# A repeat of the same request id must not generate or persist a second answer.
repeat_request_id="$request_id"
curl -sS -o "$tmp_dir/repeat.txt" -X POST "$base_url/api/v1/sessions/$conversation_id/messages" \
  "${dev_headers[@]}" -H 'Content-Type: application/json' \
  -d "{\"query\":\"how does hybrid recall combine keyword and vector results\",\"requestId\":\"$repeat_request_id\"}"
[[ "$(psql_value "SELECT count(*) FROM messages WHERE request_id = '$repeat_request_id' AND role = 'assistant';")" == "1" ]]

# ---------------------------------------------------------------------------
# Refusal: no authorized evidence must produce a refusal, never invented facts.
# ---------------------------------------------------------------------------
# The query is natural language with function words on purpose: BM25 recall must
# drop them instead of matching every chunk, otherwise no question could ever be
# refused for lack of evidence.
no_answer_request="m3-refusal-$(date +%s)"
curl -sS -N -o "$tmp_dir/refusal.txt" -X POST "$base_url/api/v1/sessions/$conversation_id/messages" \
  "${dev_headers[@]}" -H 'Content-Type: application/json' \
  -d "{\"query\":\"what is the sedimentary composition of the cretaceous layers\",\"requestId\":\"$no_answer_request\",\"sessionOverride\":\"$no_evidence_override\"}"
grep -q '"status":"REFUSED"' "$tmp_dir/refusal.txt"
grep -q '"code":"insufficient_evidence"' "$tmp_dir/refusal.txt"
python3 - "$tmp_dir/refusal.txt" <<'PY'
import json
import re
import sys

raw = open(sys.argv[1], encoding="utf-8").read()
answer = next(json.loads(re.search(r"^data: (.+)$", block, re.M).group(1))
              for block in raw.split("\n\n") if block.startswith("event: answer"))
assert answer["data"]["citations"] == [], answer
assert not re.search(r"\[\d+\]", answer["data"]["content"]), answer["data"]["content"]
assert answer["data"]["content"].strip(), "a refusal must still explain itself"
PY
[[ "$(psql_value "SELECT count(*) FROM messages WHERE request_id = '$no_answer_request' AND role = 'assistant' AND status = 'REFUSED' AND error_code = 'insufficient_evidence';")" == "1" ]]
located_message_id="$(psql_value "SELECT id FROM messages WHERE request_id = '$no_answer_request' AND role = 'assistant';")"
[[ "$(psql_value "SELECT count(*) FROM message_citations WHERE message_id = '$located_message_id';")" == "0" ]]

# ---------------------------------------------------------------------------
# Permission negative: a foreign tenant leaks nothing through any M3 surface.
# ---------------------------------------------------------------------------
request POST /api/v1/sessions 403 "$tmp_dir/cross-session.json" "${other_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"knowledge_base_id":"kb-dev"}'
request GET "/api/v1/chunks/$first_chunk_id" 404 "$tmp_dir/cross-chunk.json" "${other_headers[@]}"
request GET "/api/v1/documents/$document_id/content" 404 "$tmp_dir/cross-content.json" "${other_headers[@]}"
request GET "/api/v1/documents/$document_id/chunks" 404 "$tmp_dir/cross-chunks.json" "${other_headers[@]}"
request GET "/api/v1/knowledge-bases/kb-dev/documents" 403 "$tmp_dir/cross-documents.json" "${other_headers[@]}"
request POST /api/v1/search 403 "$tmp_dir/cross-search.json" "${other_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"query":"hybrid recall","knowledgeBaseId":"kb-dev"}'
request GET "/api/v1/requests/$request_id/events" 404 "$tmp_dir/cross-resume.json" "${other_headers[@]}"
# The foreign tenant must not be able to read an existing session either.
request GET "/api/v1/sessions/$conversation_id/messages" 404 "$tmp_dir/cross-messages.json" "${other_headers[@]}"
if grep -q "$first_chunk_id" "$tmp_dir/cross-content.json" "$tmp_dir/cross-chunks.json" "$tmp_dir/cross-search.json"; then
  echo "cross-tenant response leaked a chunk identifier" >&2
  exit 1
fi
[[ "$(psql_value "SELECT count(*) FROM conversations WHERE tenant_id = 'other-tenant';")" == "0" ]]
[[ "$(psql_value "SELECT count(*) FROM messages WHERE tenant_id = 'other-tenant';")" == "0" ]]
[[ "$(psql_value "SELECT count(*) FROM stream_events WHERE tenant_id = 'other-tenant';")" == "0" ]]
[[ "$(psql_value "SELECT count(*) FROM message_citations WHERE tenant_id = 'other-tenant';")" == "0" ]]

# ---------------------------------------------------------------------------
# Retrieval parameters: three tiers, server-side range checks and audit.
# ---------------------------------------------------------------------------
request PATCH "/api/v1/knowledge-bases/kb-dev" 400 "$tmp_dir/bad-params.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"retrieval_config":{"top_k":900}}'
request PATCH "/api/v1/knowledge-bases/kb-dev" 400 "$tmp_dir/bad-threshold.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"retrieval_config":{"vector_threshold":1.5}}'

request PATCH "/api/v1/knowledge-bases/kb-dev" 200 "$tmp_dir/params.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"retrieval_config":{"top_k":1}}'
[[ "$(psql_value "SELECT (retrieval_config->>'top_k') FROM knowledge_bases WHERE id = 'kb-dev';")" == "1" ]]
[[ "$(psql_value "SELECT count(*) FROM audit_logs WHERE resource_type = 'retrieval_config' AND resource_id = 'kb-dev' AND reason LIKE 'retrieval_config_updated%';")" -ge 1 ]]
request POST /api/v1/search 200 "$tmp_dir/search-limited.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"query":"hybrid recall keyword vector","knowledgeBaseId":"kb-dev"}'
[[ "$(json_len '["items"]' "$tmp_dir/search-limited.json")" -le 1 ]]

# A session-level override narrows the same parameter for one request only.
request POST /api/v1/search 200 "$tmp_dir/search-override.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"query":"hybrid recall keyword vector","knowledgeBaseId":"kb-dev","sessionOverride":"{\"top_k\":0}"}'
[[ "$(json_len '["items"]' "$tmp_dir/search-override.json")" == "0" ]]
request PATCH "/api/v1/knowledge-bases/kb-dev" 200 "$tmp_dir/params-restore.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"retrieval_config":{"top_k":8}}'

# ---------------------------------------------------------------------------
# Only the current version is searchable: reprocessing retires the old chunks.
# ---------------------------------------------------------------------------
printf '# Retrieval Handbook v2\n\n## Hybrid recall\nThe revised handbook no longer describes reciprocal rank fusion and instead requires manual escalation.\n' > "$tmp_dir/handbook-v2.md"
request POST /api/v1/knowledge-bases/kb-dev/documents 202 "$tmp_dir/reprocess.json" "${dev_headers[@]}" \
  -H "Idempotency-Key: m3-reprocess-$(date +%s)" -F "document_id=$document_id" -F "file=@$tmp_dir/handbook-v2.md;type=text/markdown"
new_version_id="$(json '["version"]["id"]' "$tmp_dir/reprocess.json")"
for _ in $(seq 1 40); do
  status="$(psql_value "SELECT status FROM document_versions WHERE id = '$new_version_id';")"
  [[ "$status" == "EVIDENCE_READY" ]] && break
  sleep 1
done
[[ "$status" == "EVIDENCE_READY" ]]
request POST /api/v1/search 200 "$tmp_dir/search-after-reprocess.json" "${dev_headers[@]}" \
  -H 'Content-Type: application/json' -d '{"query":"hybrid recall keyword vector","knowledgeBaseId":"kb-dev"}'
if grep -q "$version_id" "$tmp_dir/search-after-reprocess.json"; then
  echo "a superseded version is still searchable" >&2
  exit 1
fi
[[ "$(psql_value "SELECT count(*) FROM chunks WHERE document_version_id = '$version_id' AND status = 'EVIDENCE_READY';")" == "0" ]]
# The superseded version's chunk detail and content must stop being served.
request GET "/api/v1/chunks/$first_chunk_id" 409 "$tmp_dir/stale-chunk.json" "${dev_headers[@]}"

# ---------------------------------------------------------------------------
# Document list: an ingested document stays discoverable, pageable, replaceable
# and deletable after the upload response is gone.
# ---------------------------------------------------------------------------
printf '# Retention Note\n\n## Window\nEvidence is retained for thirty days.\n' > "$tmp_dir/retention.md"
request POST /api/v1/knowledge-bases/kb-dev/documents 202 "$tmp_dir/list-upload.json" "${dev_headers[@]}" \
  -H "Idempotency-Key: m3-list-$(date +%s)" -F "file=@$tmp_dir/retention.md;type=text/markdown"
listed_document_id="$(json '["document"]["id"]' "$tmp_dir/list-upload.json")"
listed_version_id="$(json '["version"]["id"]' "$tmp_dir/list-upload.json")"
for _ in $(seq 1 40); do
  listed_status="$(psql_value "SELECT status FROM document_versions WHERE id = '$listed_version_id';")"
  [[ "$listed_status" == "EVIDENCE_READY" ]] && break
  sleep 1
done
[[ "$listed_status" == "EVIDENCE_READY" ]]

# The list reports the current version, not the aggregate document row: a failed
# pipeline leaves documents.status at STORED, so the version is the only source
# the UI can trust.
request GET "/api/v1/knowledge-bases/kb-dev/documents?limit=200" 200 "$tmp_dir/list-all.json" "${dev_headers[@]}"
[[ "$(json '["schemaVersion"]' "$tmp_dir/list-all.json")" == "DocumentPageV1" ]]
[[ "$(json '["knowledgeBaseId"]' "$tmp_dir/list-all.json")" == "kb-dev" ]]
[[ "$(json '["hasMore"]' "$tmp_dir/list-all.json")" == "False" ]]
if grep -q 'sourceObjectKey\|normalizedKey\|tenants/dev-tenant' "$tmp_dir/list-all.json"; then
  echo "the document list leaked an object storage key" >&2
  exit 1
fi
python3 - "$tmp_dir/list-all.json" "$listed_document_id" "$listed_version_id" "$document_id" "$new_version_id" <<'PY'
import json
import sys

page = json.load(open(sys.argv[1], encoding="utf-8"))
by_id = {item["document"]["id"]: item for item in page["items"]}

listed = by_id.get(sys.argv[2])
assert listed, "the uploaded document must appear in the list"
assert listed["document"]["latestVersionId"] == sys.argv[3], listed
assert listed["version"]["status"] == "EVIDENCE_READY", listed
assert listed["contentReady"] is True, listed
assert listed["previewable"] is True, listed

# The document uploaded at the start of this run is still listed with the
# version that replaced its content, which is what survives a page reload.
reprocessed = by_id.get(sys.argv[4])
assert reprocessed, "the run's own document must still be listed"
assert reprocessed["document"]["latestVersionId"] == sys.argv[5], reprocessed

# Newest first, so the newest upload leads and nothing is duplicated.
assert page["items"][0]["document"]["id"] == sys.argv[2], page["items"][0]["document"]["id"]
ids = [item["document"]["id"] for item in page["items"]]
assert len(ids) == len(set(ids)), ids
PY

# Cursor paging must neither repeat nor skip a document.
request GET "/api/v1/knowledge-bases/kb-dev/documents?limit=1" 200 "$tmp_dir/list-page1.json" "${dev_headers[@]}"
[[ "$(json_len '["items"]' "$tmp_dir/list-page1.json")" == "1" ]]
[[ "$(json '["hasMore"]' "$tmp_dir/list-page1.json")" == "True" ]]
list_cursor="$(json '["nextCursor"]' "$tmp_dir/list-page1.json")"
[[ -n "$list_cursor" ]]
request GET "/api/v1/knowledge-bases/kb-dev/documents?limit=1&cursor=$list_cursor" 200 "$tmp_dir/list-page2.json" "${dev_headers[@]}"
page1_document_id="$(json '["items"][0]["document"]["id"]' "$tmp_dir/list-page1.json")"
page2_document_id="$(json '["items"][0]["document"]["id"]' "$tmp_dir/list-page2.json")"
[[ "$page1_document_id" != "$page2_document_id" ]]
if grep -q "$page1_document_id" "$tmp_dir/list-page2.json"; then
  echo "cursor paging returned a document twice" >&2
  exit 1
fi
# A malformed cursor is a client error instead of a silent restart at page one.
request GET "/api/v1/knowledge-bases/kb-dev/documents?cursor=not-a-cursor" 400 "$tmp_dir/list-bad-cursor.json" "${dev_headers[@]}"

# Deleting retires the document: it leaves the list and stops being readable.
request DELETE "/api/v1/documents/$listed_document_id" 204 "$tmp_dir/list-delete.json" "${dev_headers[@]}"
request GET "/api/v1/knowledge-bases/kb-dev/documents?limit=200" 200 "$tmp_dir/list-after-delete.json" "${dev_headers[@]}"
if grep -q "$listed_document_id" "$tmp_dir/list-after-delete.json"; then
  echo "a deleted document is still listed" >&2
  exit 1
fi
[[ "$(psql_value "SELECT count(*) FROM document_versions WHERE document_id = '$listed_document_id' AND status <> 'CANCELLED';")" == "0" ]]
[[ "$(psql_value "SELECT count(*) FROM chunks WHERE document_version_id = '$listed_version_id' AND status = 'EVIDENCE_READY';")" == "0" ]]
# M4 turns deletion into a soft delete that is invisible to every read path,
# so the document itself is now not found rather than a stale-version conflict.
request GET "/api/v1/documents/$listed_document_id/content" 404 "$tmp_dir/list-deleted-content.json" "${dev_headers[@]}"

printf 'M3 acceptance passed: document=%s version=%s newVersion=%s conversation=%s listedDocument=%s\n' "$document_id" "$version_id" "$new_version_id" "$conversation_id" "$listed_document_id"