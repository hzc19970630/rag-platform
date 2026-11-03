#!/usr/bin/env bash
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

psql_value() {
  "${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "$1"
}

curl -fsS "$base_url/readyz" | grep -q '"status":"ok"'
curl -fsS "$base_url/readyz" | python3 -c 'import json,sys; formats=set(json.load(sys.stdin)["supported_formats"]); missing={"xlsx","pptx","html"}-formats; assert not missing, missing'
"${compose[@]}" exec -T redis redis-cli ping | grep -q PONG
"${compose[@]}" exec -T minio mc alias set acceptance http://127.0.0.1:9000 "${MINIO_ROOT_USER:-rag-local}" "${MINIO_ROOT_PASSWORD:-change-me-local-only}" >/dev/null
cors_headers="$(curl -sS -D - -o /dev/null -X OPTIONS "$base_url/api/v1/knowledge-bases/kb-dev/documents" \
  -H 'Origin: http://localhost:5173' \
  -H 'Access-Control-Request-Method: POST' \
  -H 'Access-Control-Request-Headers: idempotency-key')"
printf '%s\n' "$cors_headers" | grep -i '^Access-Control-Allow-Origin: http://localhost:5173' >/dev/null
printf '%s\n' "$cors_headers" | grep -i '^Access-Control-Allow-Headers:.*idempotency-key' >/dev/null

"${compose[@]}" exec -T docreader python - <<'PY'
import io
import zipfile
from main import parse_content

assert parse_content("sample.txt", "text/plain", b"plain text")[0]
assert parse_content("sample.md", "text/markdown", b"# title\n\nmarkdown text")[0]

docx = io.BytesIO()
with zipfile.ZipFile(docx, "w") as archive:
    archive.writestr("word/document.xml", """<w:document xmlns:w=\"http://schemas.openxmlformats.org/wordprocessingml/2006/main\"><w:body><w:p><w:r><w:t>docx text</w:t></w:r></w:p></w:body></w:document>""")
assert parse_content("sample.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", docx.getvalue())[0]

def pdf_bytes(text):
    stream = ("BT /F1 12 Tf 72 720 Td (" + text + ") Tj ET\n").encode()
    objects = [
        b"<< /Type /Catalog /Pages 2 0 R >>",
        b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
        b"<< /Length " + str(len(stream)).encode() + b" >>\nstream\n" + stream + b"endstream",
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
    ]
    output = b"%PDF-1.4\n"
    offsets = [0]
    for index, value in enumerate(objects, 1):
        offsets.append(len(output))
        output += f"{index} 0 obj\n".encode() + value + b"\nendobj\n"
    xref = len(output)
    output += b"xref\n0 6\n0000000000 65535 f \n"
    output += b"".join(f"{offset:010d} 00000 n \n".encode() for offset in offsets[1:])
    output += b"trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n" + str(xref).encode() + b"\n%%EOF\n"
    return output

assert parse_content("sample.pdf", "application/pdf", pdf_bytes("digital pdf"))[0]
try:
    parse_content("scanned.pdf", "application/pdf", pdf_bytes(""))
except ValueError as error:
    assert str(error) == "unsupported_scanned_pdf"
else:
    raise AssertionError("scanned PDF must be rejected")

sheet_mime = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
slide_mime = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
SML = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
PML = "http://schemas.openxmlformats.org/presentationml/2006/main"
DML = "http://schemas.openxmlformats.org/drawingml/2006/main"
REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
PKG = "http://schemas.openxmlformats.org/package/2006/relationships"

xlsx = io.BytesIO()
with zipfile.ZipFile(xlsx, "w", zipfile.ZIP_DEFLATED) as archive:
    archive.writestr("xl/workbook.xml", f'<workbook xmlns="{SML}" xmlns:r="{REL}"><sheets><sheet name="First" sheetId="1" r:id="rId1"/><sheet name="Budget" sheetId="2" r:id="rId2"/></sheets></workbook>')
    archive.writestr("xl/_rels/workbook.xml.rels", f'<Relationships xmlns="{PKG}"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/></Relationships>')
    archive.writestr("xl/sharedStrings.xml", f'<sst xmlns="{SML}"><si><t>Report</t></si><si><t>Q1</t></si></sst>')
    archive.writestr("xl/styles.xml", f'<styleSheet xmlns="{SML}"><cellXfs count="2"><xf numFmtId="0"/><xf numFmtId="14"/></cellXfs></styleSheet>')
    # sheet2.xml is written first: sheet order must come from <sheets>, not filenames.
    archive.writestr("xl/worksheets/sheet2.xml", f'<worksheet xmlns="{SML}"><sheetData><row r="1"><c r="A1" t="s"><v>1</v></c></row></sheetData></worksheet>')
    archive.writestr("xl/worksheets/sheet1.xml", f'<worksheet xmlns="{SML}"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row><row r="2"><c r="A2" t="inlineStr"><is><t>inline note</t></is></c><c r="B2"><v>128</v></c></row><row r="3"><c r="A3" s="1"><v>45292</v></c><c r="B3" t="str"><f>SUM(B1:B2)</f><v>129</v></c></row></sheetData></worksheet>')

blocks, _ = parse_content("sample.xlsx", sheet_mime, xlsx.getvalue())
texts = [item.text for item in blocks]
assert blocks[0].heading_path == "sample / First", [item.heading_path for item in blocks]
assert blocks[0].source_path.startswith("sheet:First!"), blocks[0].source_path
assert texts[0] == "A: Report | B: Q1", texts
assert "A: inline note | B: 128" in texts, texts
assert any("2024-01-01" in text for text in texts), texts
assert any("129" in text for text in texts), texts
assert any(item.heading_path == "sample / Budget" for item in blocks), [item.heading_path for item in blocks]

def slide_xml(title, body, picture=False):
    pic = '<p:pic><p:nvPicPr><p:cNvPr id="9" name="pic"/><p:cNvPicPr/><p:nvPr/></p:nvPicPr><p:blipFill/><p:spPr/></p:pic>' if picture else ""
    return f'<p:sld xmlns:p="{PML}" xmlns:a="{DML}"><p:cSld><p:spTree><p:nvGrpSpPr/><p:grpSpPr/><p:sp><p:nvSpPr><p:cNvPr id="2" name="Title 1"/><p:cNvSpPr/><p:nvPr><p:ph type="ctrTitle"/></p:nvPr></p:nvSpPr><p:spPr/><p:txBody><a:bodyPr/><a:p><a:r><a:t>{title}</a:t></a:r></a:p></p:txBody></p:sp><p:sp><p:nvSpPr><p:cNvPr id="3" name="Body"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr><p:spPr/><p:txBody><a:bodyPr/><a:p><a:r><a:t>{body}</a:t></a:r></a:p></p:txBody></p:sp>{pic}</p:spTree></p:cSld></p:sld>'

pptx = io.BytesIO()
with zipfile.ZipFile(pptx, "w", zipfile.ZIP_DEFLATED) as archive:
    # sldIdLst references rId2 first, and rId2 maps to slide2.xml: order must
    # follow the relationship list, not the slideN.xml filename.
    archive.writestr("ppt/presentation.xml", f'<p:presentation xmlns:p="{PML}" xmlns:r="{REL}"><p:sldIdLst><p:sldId id="256" r:id="rId2"/><p:sldId id="257" r:id="rId1"/></p:sldIdLst></p:presentation>')
    archive.writestr("ppt/_rels/presentation.xml.rels", f'<Relationships xmlns="{PKG}"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide2.xml"/></Relationships>')
    archive.writestr("ppt/slides/slide1.xml", slide_xml("Second slide", "body two", picture=True))
    archive.writestr("ppt/slides/slide2.xml", slide_xml("First slide", "body one"))
    archive.writestr("ppt/slides/_rels/slide1.xml.rels", f'<Relationships xmlns="{PKG}"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/></Relationships>')
    archive.writestr("ppt/slides/_rels/slide2.xml.rels", f'<Relationships xmlns="{PKG}"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/></Relationships>')
    archive.writestr("ppt/slideLayouts/slideLayout1.xml", f'<p:sldLayout xmlns:p="{PML}" xmlns:a="{DML}"><p:cSld><p:spTree><p:sp><p:nvSpPr><p:cNvPr id="2" name="Layout Title"/><p:cNvSpPr/><p:nvPr><p:ph type="ctrTitle" idx="0"/></p:nvPr></p:nvSpPr></p:sp></p:spTree></p:cSld></p:sldLayout>')

blocks, title = parse_content("deck.pptx", slide_mime, pptx.getvalue())
assert title == "First slide", title
assert blocks[0].kind == "heading" and blocks[0].text == "First slide" and blocks[0].page_number == 1, [(item.kind, item.text, item.page_number) for item in blocks]
assert blocks[2].text == "Second slide" and blocks[2].page_number == 2, [(item.text, item.page_number) for item in blocks]
assert all("pic" not in item.text for item in blocks), [item.text for item in blocks]

html_page = b'<!DOCTYPE html><html><head><meta charset="utf-8"><title>Runbook Title</title><style>.x{color:red} style-must-not-appear</style><script>script-must-not-appear</script></head><body><h1>Runbook</h1><h2>Steps</h2><p>first<br/>second</p><table><tr><th>Name</th><th>Owner</th></tr><tr><td>Alice</td><td>Ops</td></tr></table><p>Tom &amp; Jerry</p><pre>line one\n\nline three</pre></body></html>'
blocks, title = parse_content("page.html", "text/html", html_page)
joined = "\n".join(item.text for item in blocks)
assert "style-must-not-appear" not in joined and "script-must-not-appear" not in joined, joined
assert title == "Runbook Title", title
assert blocks[0].kind == "heading" and blocks[0].text == "Runbook" and blocks[0].heading_path == "Runbook", [(item.kind, item.text, item.heading_path) for item in blocks]
assert any(item.text == "first second" for item in blocks), [item.text for item in blocks]
assert any(item.text == "Name | Owner" for item in blocks), [item.text for item in blocks]
assert any(item.text == "Tom & Jerry" for item in blocks), [item.text for item in blocks]
assert any(item.kind == "heading" and item.heading_path == "Runbook / Steps" for item in blocks), [item.heading_path for item in blocks]
assert any(item.text == "line one\n\nline three" for item in blocks), [repr(item.text) for item in blocks]
assert all(item.source_path.startswith("line:") for item in blocks), [item.source_path for item in blocks]

bom_blocks, _ = parse_content("page.htm", "text/html", b"\xef\xbb\xbf" + html_page)
assert len(bom_blocks) == len(blocks), (len(bom_blocks), len(blocks))
xhtml_blocks, xhtml_title = parse_content("page.htm", "application/xhtml+xml", b'<html xmlns="http://www.w3.org/1999/xhtml"><head><title>X</title></head><body><h1>Runbook</h1><p>a<br/>b</p></body></html>')
assert xhtml_title == "X" and any(item.text == "a b" for item in xhtml_blocks), [item.text for item in xhtml_blocks]

def expect_error(code, call):
    try:
        call()
    except ValueError as error:
        assert str(error) == code, (code, str(error))
    else:
        raise AssertionError(f"expected {code}")

expect_error("unsupported_legacy_office", lambda: parse_content("old.xls", "application/vnd.ms-excel", b""))
expect_error("unsupported_legacy_office", lambda: parse_content("old.ppt", "application/vnd.ms-powerpoint", b""))
expect_error("unsupported_corrupt_document", lambda: parse_content("broken.xlsx", sheet_mime, b"not a zip"))
expect_error("unsupported_corrupt_document", lambda: parse_content("broken.pptx", slide_mime, b"not a zip"))

oversized = io.BytesIO()
with zipfile.ZipFile(oversized, "w", zipfile.ZIP_DEFLATED) as archive:
    archive.writestr("xl/workbook.xml", b"\0" * (40 * 1024 * 1024))
expect_error("document_too_complex", lambda: parse_content("big.xlsx", sheet_mime, oversized.getvalue()))

entity = io.BytesIO()
with zipfile.ZipFile(entity, "w", zipfile.ZIP_DEFLATED) as archive:
    archive.writestr("xl/workbook.xml", b'<!DOCTYPE x [<!ENTITY a "b">]><workbook/>')
expect_error("document_too_complex", lambda: parse_content("entity.xlsx", sheet_mime, entity.getvalue()))

deep = io.BytesIO()
with zipfile.ZipFile(deep, "w", zipfile.ZIP_DEFLATED) as archive:
    archive.writestr("xl/workbook.xml", b"<a>" * 80 + b"</a>" * 80)
expect_error("document_too_complex", lambda: parse_content("deep.xlsx", sheet_mime, deep.getvalue()))
PY

printf '# M2 Acceptance\n\nDigital Markdown content must become evidence.\n' > "$tmp_dir/acceptance.md"
key="m2-acceptance-$(date +%s)"
request POST /api/v1/knowledge-bases/kb-dev/documents 202 "$tmp_dir/upload.json" \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
  -H "Idempotency-Key: $key" -F "file=@$tmp_dir/acceptance.md;type=text/markdown"

document_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["document"]["id"])' "$tmp_dir/upload.json")"
version_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"]["id"])' "$tmp_dir/upload.json")"
object_key="$(psql_value "SELECT source_object_key FROM document_versions WHERE id = '$version_id';")"
if grep -q 'sourceObjectKey\|normalizedKey' "$tmp_dir/upload.json"; then
  echo "the upload response exposed an object storage key" >&2
  exit 1
fi
"${compose[@]}" exec -T minio mc stat "acceptance/${S3_BUCKET:-rag-platform-local}/$object_key" >/dev/null
for _ in $(seq 1 30); do
  request GET "/api/v1/documents/$document_id" 200 "$tmp_dir/status.json" \
    -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant'
  pipeline_status="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["pipeline"]["status"])' "$tmp_dir/status.json")"
  [[ "$pipeline_status" == "EVIDENCE_READY" ]] && break
  if [[ "$pipeline_status" == "FAILED" ]]; then
    cat "$tmp_dir/status.json" >&2
    exit 1
  fi
  sleep 1
done
[[ "$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["pipeline"]["status"])' "$tmp_dir/status.json")" == "EVIDENCE_READY" ]]
[[ "$(python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1]))["stages"]))' "$tmp_dir/status.json")" == "5" ]]

python3 - "$tmp_dir/scanned.pdf" <<'PY'
import sys

objects = [
    b"<< /Type /Catalog /Pages 2 0 R >>",
    b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
    b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> /Contents 4 0 R >>",
    b"<< /Length 0 >>\nstream\n\nendstream",
]
output = b"%PDF-1.4\n"
offsets = [0]
for index, value in enumerate(objects, 1):
    offsets.append(len(output))
    output += f"{index} 0 obj\n".encode() + value + b"\nendobj\n"
xref = len(output)
output += b"xref\n0 5\n0000000000 65535 f \n"
output += b"".join(f"{offset:010d} 00000 n \n".encode() for offset in offsets[1:])
output += b"trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n" + str(xref).encode() + b"\n%%EOF\n"
open(sys.argv[1], "wb").write(output)
PY
scan_key="m2-scanned-$(date +%s)"
request POST /api/v1/knowledge-bases/kb-dev/documents 202 "$tmp_dir/scanned-upload.json" \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
  -H "Idempotency-Key: $scan_key" -F "file=@$tmp_dir/scanned.pdf;type=application/pdf"
scan_document_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["document"]["id"])' "$tmp_dir/scanned-upload.json")"
for _ in $(seq 1 30); do
  request GET "/api/v1/documents/$scan_document_id" 200 "$tmp_dir/scanned-status.json" \
    -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant'
  scan_status="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["pipeline"]["status"])' "$tmp_dir/scanned-status.json")"
  [[ "$scan_status" == "FAILED" ]] && break
  sleep 1
done
[[ "$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["pipeline"]["errorCode"])' "$tmp_dir/scanned-status.json")" == "unsupported_scanned_pdf" ]]
curl -fsS "$base_url/readyz" | grep -q '"status":"ok"'

# xlsx and html format coverage runs in a throwaway knowledge base, then deletes
# it, so the kb-dev fixtures above stay untouched.
request POST /api/v1/knowledge-bases 201 "$tmp_dir/formats-kb.json" \
  -H 'Content-Type: application/json' \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
  -d '{"name":"M2 Formats Acceptance","description":"xlsx and html parser coverage"}'
formats_kb="$(python3 -c 'import json,sys; data=json.load(open(sys.argv[1])); print(data.get("id") or data["ID"])' "$tmp_dir/formats-kb.json")"

python3 - "$tmp_dir/report.xlsx" <<'PY'
import sys
import zipfile
from datetime import date

SML = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
PKG = "http://schemas.openxmlformats.org/package/2006/relationships"
serial = (date(2024, 6, 30) - date(1899, 12, 30)).days

with zipfile.ZipFile(sys.argv[1], "w", zipfile.ZIP_DEFLATED) as archive:
    archive.writestr("xl/workbook.xml", f'<workbook xmlns="{SML}" xmlns:r="{REL}"><sheets><sheet name="Budget" sheetId="1" r:id="rId1"/></sheets></workbook>')
    archive.writestr("xl/_rels/workbook.xml.rels", f'<Relationships xmlns="{PKG}"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>')
    archive.writestr("xl/sharedStrings.xml", f'<sst xmlns="{SML}"><si><t>Budget Report</t></si><si><t>Owner</t></si></sst>')
    archive.writestr("xl/styles.xml", f'<styleSheet xmlns="{SML}"><cellXfs count="2"><xf numFmtId="0"/><xf numFmtId="14"/></cellXfs></styleSheet>')
    archive.writestr("xl/worksheets/sheet1.xml", f'<worksheet xmlns="{SML}"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row><row r="2"><c r="A2" s="1"><v>{serial}</v></c><c r="B2"><v>128</v></c></row></sheetData></worksheet>')
PY

request POST "/api/v1/knowledge-bases/$formats_kb/documents" 202 "$tmp_dir/xlsx-upload.json" \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
  -H "Idempotency-Key: m2-xlsx-$(date +%s)" \
  -F "file=@$tmp_dir/report.xlsx;type=application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
xlsx_document="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["document"]["id"])' "$tmp_dir/xlsx-upload.json")"
xlsx_version="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"]["id"])' "$tmp_dir/xlsx-upload.json")"
wait_ready() {
  local document="$1" output="$2" status
  for _ in $(seq 1 30); do
    request GET "/api/v1/documents/$document" 200 "$output" \
      -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant'
    status="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["pipeline"]["status"])' "$output")"
    [[ "$status" == "EVIDENCE_READY" ]] && return 0
    if [[ "$status" == "FAILED" ]]; then
      cat "$output" >&2
      return 1
    fi
    sleep 1
  done
  return 1
}
wait_ready "$xlsx_document" "$tmp_dir/xlsx-status.json"
[[ "$(psql_value "SELECT count(*) FROM chunks WHERE document_version_id = '$xlsx_version' AND heading_path LIKE '%Budget%';")" -gt 0 ]]
[[ "$(psql_value "SELECT count(*) FROM chunks WHERE document_version_id = '$xlsx_version' AND content LIKE '%2024-06-30%';")" -gt 0 ]]

xlsx_preview_code="$(curl -sS -D "$tmp_dir/xlsx-preview.headers" -o "$tmp_dir/xlsx-preview.bin" -w '%{http_code}' \
  "$base_url/api/v1/documents/$xlsx_document/preview" \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant')"
[[ "$xlsx_preview_code" == "200" ]]
grep -i '^Content-Disposition: attachment' "$tmp_dir/xlsx-preview.headers" >/dev/null
grep -i '^Content-Type: application/vnd.openxmlformats-officedocument.spreadsheetml.sheet' "$tmp_dir/xlsx-preview.headers" >/dev/null
grep -i '^X-Content-Type-Options: nosniff' "$tmp_dir/xlsx-preview.headers" >/dev/null

printf '<html><head><title>Runbook</title></head><body><h1>Runbook</h1><p>Hello <b>world</b>.</p><script>alert(1)</script></body></html>' > "$tmp_dir/page.html"
request POST "/api/v1/knowledge-bases/$formats_kb/documents" 202 "$tmp_dir/html-upload.json" \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
  -H "Idempotency-Key: m2-html-$(date +%s)" -F "file=@$tmp_dir/page.html;type=text/html"
html_document="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["document"]["id"])' "$tmp_dir/html-upload.json")"
html_version="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"]["id"])' "$tmp_dir/html-upload.json")"
wait_ready "$html_document" "$tmp_dir/html-status.json"
[[ "$(psql_value "SELECT count(*) FROM chunks WHERE document_version_id = '$html_version' AND content LIKE '%Hello%';")" -gt 0 ]]
[[ "$(psql_value "SELECT count(*) FROM chunks WHERE document_version_id = '$html_version' AND content LIKE '%<h1>%';")" == "0" ]]
[[ "$(psql_value "SELECT count(*) FROM chunks WHERE document_version_id = '$html_version' AND content LIKE '%alert(1)%';")" == "0" ]]

html_preview_code="$(curl -sS -D "$tmp_dir/html-preview.headers" -o "$tmp_dir/html-preview.bin" -w '%{http_code}' \
  "$base_url/api/v1/documents/$html_document/preview" \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant')"
[[ "$html_preview_code" == "200" ]]
grep -i '^Content-Disposition: attachment' "$tmp_dir/html-preview.headers" >/dev/null
grep -i '^Content-Type: text/html' "$tmp_dir/html-preview.headers" >/dev/null
grep -i '^X-Content-Type-Options: nosniff' "$tmp_dir/html-preview.headers" >/dev/null

printf 'legacy office' > "$tmp_dir/legacy.xls"
request POST "/api/v1/knowledge-bases/$formats_kb/documents" 400 "$tmp_dir/legacy-xls.json" \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
  -H "Idempotency-Key: m2-xls-$(date +%s)" -F "file=@$tmp_dir/legacy.xls;type=application/vnd.ms-excel"
printf 'legacy office' > "$tmp_dir/legacy.ppt"
request POST "/api/v1/knowledge-bases/$formats_kb/documents" 400 "$tmp_dir/legacy-ppt.json" \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
  -H "Idempotency-Key: m2-ppt-$(date +%s)" -F "file=@$tmp_dir/legacy.ppt;type=application/vnd.ms-powerpoint"
printf '\x89PNG\r\n\x1a\n' > "$tmp_dir/scan.png"
request POST "/api/v1/knowledge-bases/$formats_kb/documents" 400 "$tmp_dir/png.json" \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
  -H "Idempotency-Key: m2-png-$(date +%s)" -F "file=@$tmp_dir/scan.png;type=image/png"

request DELETE "/api/v1/knowledge-bases/$formats_kb" 204 "$tmp_dir/formats-kb-delete.json" \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant'

db_total_before_duplicate="$("${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "SELECT count(*) FROM chunks WHERE document_version_id = '$version_id';")"
request POST /api/v1/knowledge-bases/kb-dev/documents 202 "$tmp_dir/duplicate.json" \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
  -H "Idempotency-Key: $key" -F "file=@$tmp_dir/acceptance.md;type=text/markdown"
duplicate_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["document"]["id"])' "$tmp_dir/duplicate.json")"
[[ "$duplicate_id" == "$document_id" ]]
db_total_after_duplicate="$("${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "SELECT count(*) FROM chunks WHERE document_version_id = '$version_id';")"
[[ "$db_total_after_duplicate" == "$db_total_before_duplicate" ]]
db_count="$("${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "SELECT count(*) FROM chunks WHERE document_version_id = '$version_id' AND status = 'EVIDENCE_READY';")"
[[ "$db_count" -gt 0 ]]
package_count="$("${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "SELECT count(*) FROM evidence_packages WHERE document_version_id = '$version_id' AND status = 'EVIDENCE_READY';")"
[[ "$package_count" == "1" ]]
outbox_pending="$("${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "SELECT count(*) FROM outbox_events WHERE aggregate_id = '$version_id' AND published_at IS NULL;")"
[[ "$outbox_pending" == "0" ]]
inbox_count="$("${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "SELECT count(*) FROM task_inbox WHERE idempotency_key LIKE (SELECT id || ':%' FROM pipeline_runs WHERE document_version_id = '$version_id');")"
[[ "$inbox_count" == "5" ]]

printf '# M2 Reprocess\n\nThis content replaces the first version and must supersede its chunks.\n' > "$tmp_dir/reprocess.md"
reprocess_key="m2-reprocess-$(date +%s)"
request POST /api/v1/knowledge-bases/kb-dev/documents 202 "$tmp_dir/reprocess.json" \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
  -H "Idempotency-Key: $reprocess_key" -F "document_id=$document_id" -F "file=@$tmp_dir/reprocess.md;type=text/markdown"
new_version_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"]["id"])' "$tmp_dir/reprocess.json")"
new_object_key="$(psql_value "SELECT source_object_key FROM document_versions WHERE id = '$new_version_id';")"
for _ in $(seq 1 30); do
  request GET "/api/v1/documents/$document_id" 200 "$tmp_dir/reprocess-status.json" \
    -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant'
  reprocess_status="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["pipeline"]["status"])' "$tmp_dir/reprocess-status.json")"
  [[ "$reprocess_status" == "EVIDENCE_READY" ]] && break
  if [[ "$reprocess_status" == "FAILED" ]]; then
    cat "$tmp_dir/reprocess-status.json" >&2
    exit 1
  fi
  sleep 1
done
[[ "$("${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "SELECT status FROM document_versions WHERE id = '$version_id';")" == "CANCELLED" ]]
[[ "$("${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "SELECT count(*) FROM chunks WHERE document_version_id = '$version_id' AND status = 'STALE';")" -gt 0 ]]
[[ "$("${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "SELECT status FROM document_versions WHERE id = '$new_version_id';")" == "EVIDENCE_READY" ]]
[[ "$("${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "SELECT count(*) FROM evidence_packages WHERE document_version_id = '$new_version_id' AND status = 'EVIDENCE_READY';")" == "1" ]]

request POST /api/v1/knowledge-bases/kb-dev/documents 403 "$tmp_dir/cross-tenant.json" \
  -H 'X-Dev-User-ID: other-user' -H 'X-Dev-Tenant-ID: other-tenant' \
  -H "Idempotency-Key: $key-cross" -F "file=@$tmp_dir/acceptance.md;type=text/markdown"
request GET "/api/v1/documents/$document_id" 404 "$tmp_dir/cross-read.json" \
  -H 'X-Dev-User-ID: other-user' -H 'X-Dev-Tenant-ID: other-tenant'

# Object storage authorization: object keys are tenant-scoped, and a foreign
# tenant can neither read nor delete another tenant's document or its object.
bucket="${S3_BUCKET:-rag-platform-local}"
case "$new_object_key" in
  tenants/dev-tenant/knowledge-bases/kb-dev/*) ;;
  *) echo "source object key is not tenant-scoped: $new_object_key" >&2; exit 1 ;;
esac
"${compose[@]}" exec -T minio mc stat "acceptance/$bucket/$new_object_key" >/dev/null

# The identical object path under another tenant prefix must not resolve.
if "${compose[@]}" exec -T minio mc stat "acceptance/$bucket/${new_object_key/dev-tenant/other-tenant}" >/dev/null 2>&1; then
  echo "cross-tenant object path unexpectedly exists for key: $new_object_key" >&2
  exit 1
fi

# A foreign tenant must not read the document or its object key, and must not
# be able to delete it.
request GET "/api/v1/documents/$document_id" 404 "$tmp_dir/cross-read-content.json" \
  -H 'X-Dev-User-ID: other-user' -H 'X-Dev-Tenant-ID: other-tenant'
if grep -q "$new_object_key" "$tmp_dir/cross-read-content.json"; then
  echo "cross-tenant read leaked the object key" >&2
  exit 1
fi
request DELETE "/api/v1/documents/$document_id" 404 "$tmp_dir/cross-delete.json" \
  -H 'X-Dev-User-ID: other-user' -H 'X-Dev-Tenant-ID: other-tenant'
"${compose[@]}" exec -T minio mc stat "acceptance/$bucket/$new_object_key" >/dev/null

# No object may be written under a foreign tenant prefix, and no foreign-tenant
# document version may exist after the attempted cross-tenant operations.
other_tenant_objects="$("${compose[@]}" exec -T minio mc ls --recursive "acceptance/$bucket/tenants/other-tenant/" 2>/dev/null || true)"
[[ -z "$other_tenant_objects" ]]
[[ "$("${compose[@]}" exec -T postgres psql -U rag_platform -d rag_platform -Atc "SELECT count(*) FROM document_versions WHERE tenant_id = 'other-tenant';")" == "0" ]]

request POST /api/v1/knowledge-bases/kb-dev/documents 400 "$tmp_dir/bad-mime.json" \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
  -H "Idempotency-Key: $key-bad" -F "file=@$tmp_dir/acceptance.md;type=application/octet-stream"

oversized_bytes=$(( ${DOCREADER_MAX_FILE_MB:-100} * 1024 * 1024 + 1 ))
truncate -s "$oversized_bytes" "$tmp_dir/oversized.txt"
request POST /api/v1/knowledge-bases/kb-dev/documents 400 "$tmp_dir/oversized.json" \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant' \
  -H "Idempotency-Key: $key-oversized" -F "file=@$tmp_dir/oversized.txt;type=text/plain"

# M4 turns deletion into a soft delete with a retention window: the document
# disappears from every read surface immediately, while versions, chunks and the
# source object are retained so it can be restored. Purging those is asserted in
# m4_acceptance.sh.
request DELETE "/api/v1/documents/$document_id" 204 "$tmp_dir/delete.json" \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant'
[[ "$(psql_value "SELECT count(*) FROM documents WHERE id = '$document_id' AND deleted_at IS NOT NULL AND purge_after IS NOT NULL;")" == "1" ]]
[[ "$(psql_value "SELECT count(*) FROM chunks WHERE document_version_id = '$new_version_id';")" -gt 0 ]]
[[ "$(psql_value "SELECT status FROM evidence_packages WHERE document_version_id = '$new_version_id';")" == "EVIDENCE_READY" ]]
"${compose[@]}" exec -T minio mc stat "acceptance/${S3_BUCKET:-rag-platform-local}/$new_object_key" >/dev/null
request GET "/api/v1/documents/$document_id" 404 "$tmp_dir/deleted-read.json" \
  -H 'X-Dev-User-ID: dev-user' -H 'X-Dev-Tenant-ID: dev-tenant'

printf 'M2 acceptance passed: document=%s version=%s chunks=%s\n' "$document_id" "$version_id" "$db_count"
