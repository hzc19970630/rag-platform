import csv
import hashlib
import html.parser
import http.server
import io
import json
import os
import posixpath
import re
import sys
import tempfile
import threading
import time
import zipfile
from concurrent import futures
from datetime import date, timedelta
from xml.etree import ElementTree

import grpc
from pypdf import PdfReader

sys.path.insert(0, "/app/generated")
import docreader_pb2  # type: ignore
import docreader_pb2_grpc  # type: ignore

def _env_int(name, default):
    try:
        return int(os.getenv(name, str(default)))
    except ValueError:
        return default


PARSER_VERSION = os.getenv("PARSER_VERSION", "docreader-m2-v3")
SCHEMA_VERSION = "NormalizedDocumentV1"

WORD_MIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
SHEET_MIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
SLIDE_MIME = "application/vnd.openxmlformats-officedocument.presentationml.presentation"

NS_SML = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
NS_PML = "http://schemas.openxmlformats.org/presentationml/2006/main"
NS_DML = "http://schemas.openxmlformats.org/drawingml/2006/main"
NS_OFFICE_REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
NS_PKG_REL = "http://schemas.openxmlformats.org/package/2006/relationships"

MIB = 1024 * 1024
MAX_FILE_BYTES = _env_int("DOCREADER_MAX_FILE_MB", 100) * MIB
MAX_ARCHIVE_ENTRIES = _env_int("DOCREADER_MAX_ARCHIVE_ENTRIES", 2048)
MAX_PART_BYTES = _env_int("DOCREADER_MAX_PART_BYTES", 64 * MIB)
MAX_ARCHIVE_BYTES = _env_int("DOCREADER_MAX_ARCHIVE_BYTES", 2048 * MIB)
MAX_READ_BYTES = _env_int("DOCREADER_MAX_READ_BYTES", 1024 * MIB)
MAX_COMPRESSION_RATIO = _env_int("DOCREADER_MAX_COMPRESSION_RATIO", 1000)
MAX_XML_DEPTH = 64
MAX_TOTAL_BLOCKS = _env_int("DOCREADER_MAX_TABLE_BLOCKS", 5000)
MAX_BLOCK_CHARS = 8000
MAX_XLSX_SHEETS = _env_int("DOCREADER_MAX_TABLE_SHEETS", 256)
MAX_XLSX_ROWS = _env_int("DOCREADER_MAX_TABLE_ROWS_PER_SHEET", 50000)
MAX_TABLE_ROWS = _env_int("DOCREADER_MAX_TABLE_ROWS", 500000)
MAX_TABLE_WINDOW_ROWS = _env_int("DOCREADER_TABLE_WINDOW_ROWS", 200)
MAX_XLSX_COLUMNS = 128
MAX_CELL_CHARS = 4096
MAX_PPTX_SLIDES = 500
MAX_SLIDE_SHAPES = 200
MAX_SHAPE_PARAGRAPHS = 200
MAX_HTML_BYTES = 16 * MIB

def _spool_writable(path):
    if not os.path.isdir(path):
        return False
    try:
        fd, probe = tempfile.mkstemp(prefix="spool-probe-", dir=path)
        os.close(fd)
        os.unlink(probe)
        return True
    except OSError:
        return False


def _select_spool_dir():
    candidates = []
    configured = os.getenv("DOCREADER_SPOOL_DIR", "/spool")
    if configured:
        candidates.append(configured)
    candidates.append(tempfile.gettempdir())
    for candidate in candidates:
        if _spool_writable(candidate):
            return candidate, True
    return tempfile.gettempdir(), False


SPOOL_DIR, SPOOL_READY = _select_spool_dir()
SPOOL_MAX_BYTES = _env_int("DOCREADER_SPOOL_MB", 384) * MIB
SPOOL_SEMAPHORE = threading.BoundedSemaphore(_env_int("DOCREADER_SPOOL_CONCURRENCY", 2))
SPOOL_WAIT_SECONDS = _env_int("DOCREADER_SPOOL_WAIT_SECONDS", 60)
DOCREADER_BLOCK_BATCH = _env_int("DOCREADER_BLOCK_BATCH", 200)
_SPOOL_LOCK = threading.Lock()
_SPOOL_USED = 0
_STATS = threading.local()


def _stats_reset():
    _STATS.truncated = False
    _STATS.total_rows = 0
    _STATS.indexed_rows = 0
    _STATS.total_sheets = 0
    _STATS.indexed_sheets = 0


def _stats_add(total_rows=0, indexed_rows=0, total_sheets=0, indexed_sheets=0, truncated=False):
    _STATS.total_rows = getattr(_STATS, "total_rows", 0) + total_rows
    _STATS.indexed_rows = getattr(_STATS, "indexed_rows", 0) + indexed_rows
    _STATS.total_sheets = getattr(_STATS, "total_sheets", 0) + total_sheets
    _STATS.indexed_sheets = getattr(_STATS, "indexed_sheets", 0) + indexed_sheets
    _STATS.truncated = getattr(_STATS, "truncated", False) or truncated


def _stats_snapshot():
    return {
        "truncated": getattr(_STATS, "truncated", False),
        "total_rows": getattr(_STATS, "total_rows", 0),
        "indexed_rows": getattr(_STATS, "indexed_rows", 0),
        "total_sheets": getattr(_STATS, "total_sheets", 0),
        "indexed_sheets": getattr(_STATS, "indexed_sheets", 0),
    }

CONTROL_CHARS = re.compile(r"[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")
WHITESPACE = re.compile(r"\s+")
CELL_REF = re.compile(r"^([A-Z]+)([0-9]+)$")
META_CHARSET = re.compile(rb"""<meta[^>]+charset=["']?([A-Za-z0-9_.:-]+)""", re.IGNORECASE)

BUILTIN_DATE_FORMATS = {14, 15, 16, 17, 22}
DATE_TOKENS = re.compile(r"[yYmMdDhHsS]")
QUOTED_LITERAL = re.compile(r'"[^"]*"')
BRACKET_SECTION = re.compile(r"\[[^\]]*\]")
EXCEL_EPOCH = date(1899, 12, 30)

HTML_SKIP_TAGS = {
    "script", "style", "noscript", "template", "svg", "canvas", "iframe",
    "object", "embed", "form", "select", "textarea", "button",
}
HTML_HEADING_TAGS = {"h1": 1, "h2": 2, "h3": 3, "h4": 4, "h5": 5, "h6": 6}
HTML_BLOCK_TAGS = {
    "p", "li", "div", "section", "article", "header", "footer", "blockquote",
    "dd", "dt", "figcaption", "nav", "aside", "main", "ul", "ol", "dl", "table",
}


def block(block_id, kind, text, heading_path="", page_number=0, source_path="", start=0, end=0):
    return docreader_pb2.DocumentBlock(
        block_id=block_id, kind=kind, text=text, heading_path=heading_path,
        page_number=page_number, source_path=source_path,
        start_offset=start, end_offset=end,
    )


def _source_bytes(source):
    if isinstance(source, (bytes, bytearray, memoryview)):
        return bytes(source)
    with open(source, "rb") as handle:
        return handle.read()


def parse_markdown(source):
    content = _source_bytes(source)
    blocks, heading, offset = [], [], 0
    for index, raw in enumerate(content.decode("utf-8-sig").splitlines(keepends=True)):
        line = raw.rstrip("\r\n")
        match = re.match(r"^(#{1,6})\s+(.+?)\s*$", line)
        if match:
            level = len(match.group(1))
            heading = heading[: level - 1] + [match.group(2)]
            blocks.append(block(f"block-{index}", "heading", match.group(2), " / ".join(heading), 0, f"line:{index + 1}", offset, offset + len(line)))
        elif line.strip():
            blocks.append(block(f"block-{index}", "paragraph", line.strip(), " / ".join(heading), 0, f"line:{index + 1}", offset, offset + len(line)))
        offset += len(raw)
    return blocks, heading[0] if heading else ""


def parse_text(source):
    content = _source_bytes(source)
    blocks, offset = [], 0
    for index, raw in enumerate(content.decode("utf-8-sig").splitlines(keepends=True)):
        line = raw.rstrip("\r\n")
        if line.strip():
            blocks.append(block(f"block-{index}", "paragraph", line.strip(), "", 0, f"line:{index + 1}", offset, offset + len(line)))
        offset += len(raw)
    return blocks, ""


def parse_docx(source):
    with _OoxmlArchive(source) as archive:
        xml = archive.read("word/document.xml")
    root = ElementTree.fromstring(xml)
    ns = {"w": "http://schemas.openxmlformats.org/wordprocessingml/2006/main"}
    blocks = []
    for index, paragraph in enumerate(root.findall(".//w:body/w:p", ns)):
        text = "".join(node.text or "" for node in paragraph.findall(".//w:t", ns)).strip()
        if not text:
            continue
        style = paragraph.find("./w:pPr/w:pStyle", ns)
        style_name = style.attrib.get("{http://schemas.openxmlformats.org/wordprocessingml/2006/main}val", "") if style is not None else ""
        kind = "heading" if style_name.lower().startswith("heading") else "paragraph"
        blocks.append(block(f"block-{index}", kind, text, "", 0, f"paragraph:{index + 1}"))
    return blocks, ""


def parse_pdf(source):
    reader = PdfReader(source if not isinstance(source, (bytes, bytearray, memoryview)) else io.BytesIO(source))
    blocks = []
    for page_index, page in enumerate(reader.pages):
        text = (page.extract_text() or "").strip()
        if not text:
            raise ValueError("unsupported_scanned_pdf")
        blocks.append(block(f"page-{page_index + 1}", "paragraph", text, "", page_index + 1, f"page:{page_index + 1}"))
    if not blocks:
        raise ValueError("unsupported_scanned_pdf")
    return blocks, ""


def _clean(value):
    return CONTROL_CHARS.sub("", value or "")


def _collapse(value):
    return WHITESPACE.sub(" ", _clean(value)).strip()


class _OoxmlArchive:
    def __init__(self, source):
        try:
            if isinstance(source, (bytes, bytearray, memoryview)):
                self._zip = zipfile.ZipFile(io.BytesIO(source))
            else:
                self._zip = zipfile.ZipFile(source)
        except (zipfile.BadZipFile, zipfile.LargeZipFile, RuntimeError, OSError, EOFError, ValueError) as error:
            raise ValueError("unsupported_corrupt_document") from error
        infos = self._zip.infolist()
        if len(infos) > MAX_ARCHIVE_ENTRIES:
            raise ValueError("document_too_complex")
        declared = 0
        for info in infos:
            if info.file_size > MAX_PART_BYTES:
                raise ValueError("document_too_complex")
            if info.file_size > 0 and info.file_size > max(info.compress_size, 1) * MAX_COMPRESSION_RATIO:
                raise ValueError("document_too_complex")
            declared += info.file_size
        if declared > MAX_ARCHIVE_BYTES:
            raise ValueError("document_too_complex")
        self.names = {info.filename for info in infos}
        self._read_total = 0

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        self._zip.close()
        return False

    def has(self, name):
        return name in self.names

    def open_stream(self, name):
        if name not in self.names:
            raise ValueError("unsupported_corrupt_document")
        info = self._zip.getinfo(name)
        self._read_total += info.file_size
        if self._read_total > MAX_READ_BYTES:
            raise ValueError("document_too_complex")
        return self._zip.open(name)

    def read(self, name):
        if name not in self.names:
            raise ValueError("unsupported_corrupt_document")
        info = self._zip.getinfo(name)
        self._read_total += info.file_size
        if self._read_total > MAX_ARCHIVE_BYTES:
            raise ValueError("document_too_complex")
        try:
            return self._zip.read(name)
        except (zipfile.BadZipFile, zipfile.LargeZipFile, RuntimeError, OSError, EOFError, ValueError) as error:
            raise ValueError("unsupported_corrupt_document") from error

    def read_xml(self, name):
        return _xml(self.read(name))


def _xml(blob):
    head = blob[:8192].lower()
    if b"<!doctype" in head or b"<!entity" in head:
        raise ValueError("document_too_complex")
    if blob.startswith(b"\xef\xbb\xbf"):
        blob = blob[3:]
    try:
        root = ElementTree.fromstring(blob)
    except ElementTree.ParseError as error:
        raise ValueError("unsupported_corrupt_document") from error
    stack = [(root, 1)]
    while stack:
        node, depth = stack.pop()
        if depth > MAX_XML_DEPTH:
            raise ValueError("document_too_complex")
        for child in node:
            stack.append((child, depth + 1))
    return root


def _resolve_target(base_dir, target):
    if target.startswith("/"):
        return posixpath.normpath(target.lstrip("/"))
    return posixpath.normpath(posixpath.join(base_dir, target))


def _relationships(archive, part_name, base_dir):
    directory, name = posixpath.split(part_name)
    rels_name = posixpath.join(directory, "_rels", name + ".rels")
    if not archive.has(rels_name):
        return {}
    root = archive.read_xml(rels_name)
    mapping = {}
    for rel in root.findall(f"{{{NS_PKG_REL}}}Relationship"):
        rel_id = rel.attrib.get("Id", "")
        target = rel.attrib.get("Target", "")
        if not rel_id or not target or rel.attrib.get("TargetMode") == "External":
            continue
        mapping[rel_id] = (rel.attrib.get("Type", ""), _resolve_target(base_dir, target))
    return mapping


def _rich_text(node):
    parts = []
    for child in node:
        if child.tag == f"{{{NS_SML}}}t":
            parts.append(child.text or "")
        elif child.tag == f"{{{NS_SML}}}r":
            parts.extend(part.text or "" for part in child.iter(f"{{{NS_SML}}}t"))
    return _clean("".join(parts))


def _shared_strings(archive):
    if not archive.has("xl/sharedStrings.xml"):
        return []
    root = archive.read_xml("xl/sharedStrings.xml")
    return [_rich_text(node) for node in root.findall(f"{{{NS_SML}}}si")]


def _style_date_flags(archive):
    if not archive.has("xl/styles.xml"):
        return []
    root = archive.read_xml("xl/styles.xml")
    custom = {}
    for fmt in root.findall(f"{{{NS_SML}}}numFmts/{{{NS_SML}}}numFmt"):
        try:
            fmt_id = int(fmt.attrib.get("numFmtId", ""))
        except ValueError:
            continue
        code = BRACKET_SECTION.sub("", fmt.attrib.get("formatCode", ""))
        custom[fmt_id] = bool(DATE_TOKENS.search(QUOTED_LITERAL.sub("", code)))
    flags = []
    for xf in root.findall(f"{{{NS_SML}}}cellXfs/{{{NS_SML}}}xf"):
        try:
            fmt_id = int(xf.attrib.get("numFmtId", "0"))
        except ValueError:
            fmt_id = 0
        flags.append(fmt_id in BUILTIN_DATE_FORMATS or custom.get(fmt_id, False))
    return flags


def _format_serial(serial):
    try:
        value = float(serial)
    except (TypeError, ValueError):
        return None
    if value < 0 or value >= 2958466:
        return None
    days = int(value)
    minutes = int(round((value - days) * 1440))
    if minutes >= 1440:
        days += 1
        minutes -= 1440
    try:
        day = EXCEL_EPOCH + timedelta(days=days)
    except OverflowError:
        return None
    if days == 0:
        return f"{minutes // 60:02d}:{minutes % 60:02d}" if minutes else None
    if minutes:
        return f"{day.isoformat()} {minutes // 60:02d}:{minutes % 60:02d}"
    return day.isoformat()


def _column_letter(index):
    letters = ""
    index += 1
    while index:
        index, remainder = divmod(index - 1, 26)
        letters = chr(65 + remainder) + letters
    return letters


def _column_index(letters):
    value = 0
    for char in letters:
        value = value * 26 + (ord(char) - 64)
    return value - 1


def _cell_text(cell, shared, style_dates):
    cell_type = cell.attrib.get("t", "n")
    value_node = cell.find(f"{{{NS_SML}}}v")
    raw = value_node.text if value_node is not None else None
    if cell_type == "s":
        try:
            index = int(raw)
        except (TypeError, ValueError):
            raise ValueError("unsupported_corrupt_document")
        if index < 0 or index >= len(shared):
            raise ValueError("unsupported_corrupt_document")
        return shared[index]
    if cell_type == "inlineStr":
        node = cell.find(f"{{{NS_SML}}}is")
        return _rich_text(node) if node is not None else ""
    if cell_type in ("str", "e", "d"):
        return _clean(raw or "").strip()
    if cell_type == "b":
        return "TRUE" if (raw or "").strip() == "1" else "FALSE"
    if raw is None:
        formula = cell.find(f"{{{NS_SML}}}f")
        if formula is not None and formula.text:
            return "=" + formula.text.strip()
        return ""
    text = _clean(raw).strip()
    try:
        style_index = int(cell.attrib.get("s", "0"))
    except ValueError:
        style_index = 0
    if 0 <= style_index < len(style_dates) and style_dates[style_index]:
        converted = _format_serial(text)
        if converted is not None:
            return converted
    return text


def _table_cell(value):
    text = _collapse(value or "").replace("|", "\\|")
    return text[:MAX_CELL_CHARS]


def _normalize_table_headers(values):
    headers = []
    seen = {}
    for index, value in enumerate(values):
        name = _table_cell(value) or _column_letter(index)
        count = seen.get(name, 0) + 1
        seen[name] = count
        headers.append(name if count == 1 else f"{name}_{count}")
    return headers


def _emit_table_blocks(prefix, heading, sheet, headers, rows, start_row, block_budget):
    blocks = []
    current = []
    header_line = " | ".join(headers)
    chars = len(header_line) + 1
    start = start_row
    end = start_row - 1
    index = 0

    def flush():
        nonlocal current, chars, index
        if not current:
            return
        text = "\n".join([header_line] + [" | ".join(row) for row in current])
        blocks.append(block(
            f"{prefix}-{index}", "table", text[:MAX_BLOCK_CHARS], heading, 0,
            f"sheet:{sheet}!A{start}:{_column_letter(max(len(headers) - 1, 0))}{end}",
        ))
        index += 1
        current = []
        chars = len(header_line) + 1

    for offset, row in enumerate(rows):
        line = " | ".join(row)
        row_number = start_row + offset
        if current and (len(current) >= MAX_TABLE_WINDOW_ROWS or chars + len(line) + 1 > MAX_BLOCK_CHARS):
            flush()
        if not current:
            start = row_number
        current.append(row)
        end = row_number
        chars += len(line) + 1
        if block_budget > 0 and len(blocks) >= block_budget:
            flush()
            blocks.append(block(f"{prefix}-truncated", "table", "[truncated: block limit reached]", heading, 0, f"sheet:{sheet}!truncated"))
            return blocks
    flush()
    return blocks[:block_budget] if block_budget > 0 else blocks


def _iter_sheet_rows(archive, target):
    with archive.open_stream(target) as stream:
        for _, element in ElementTree.iterparse(stream, events=("end",)):
            if element.tag != f"{{{NS_SML}}}row":
                continue
            try:
                row_number = int(element.attrib.get("r", "0"))
            except ValueError:
                row_number = 0
            cells = []
            for index, cell in enumerate(element.findall(f"{{{NS_SML}}}c")):
                reference = CELL_REF.match(cell.attrib.get("r", "").upper())
                column = _column_index(reference.group(1)) if reference else index
                if column >= MAX_XLSX_COLUMNS:
                    continue
                value = _cell_text(cell, archive.shared, archive.style_dates)
                if value:
                    cells.append((column, value))
            element.clear()
            yield row_number, cells


def _sheet_blocks(archive, heading_root, sheet_name, target, prefix, block_budget):
    heading = f"{heading_root} / {sheet_name}"
    headers = None
    rows = []
    indexed = 0
    total = 0
    start_row = 1
    truncated = False
    for position, (row_number, cells) in enumerate(_iter_sheet_rows(archive, target)):
        row_number = row_number or position + 1
        if not cells:
            continue
        total += 1
        if headers is None:
            width = max(column for column, _ in cells) + 1
            header_values = [""] * width
            for column, value in cells:
                header_values[column] = value
            headers = _normalize_table_headers(header_values)
            start_row = row_number + 1
            continue
        row = [""] * len(headers)
        for column, value in cells:
            if column < len(headers):
                row[column] = _table_cell(value)
        rows.append(row)
        indexed += 1
        if indexed >= MAX_XLSX_ROWS or total >= MAX_TABLE_ROWS:
            truncated = True
            break
    blocks = []
    if headers is not None and not rows:
        blocks = [block(f"{prefix}-0", "table", " | ".join(headers), heading, 0, f"sheet:{sheet_name}!A{start_row}:A{start_row}")]
    elif headers is not None and rows:
        blocks = _emit_table_blocks(prefix, heading, sheet_name, headers, rows, start_row, block_budget)
    if truncated:
        blocks.append(block(f"{prefix}-truncated", "table", "[truncated: additional rows not indexed]", heading, 0, f"sheet:{sheet_name}!truncated"))
    return blocks, total, indexed, truncated


def parse_xlsx(source, filename):
    stem = os.path.splitext(os.path.basename(filename))[0] or "workbook"
    with _OoxmlArchive(source) as archive:
        workbook = archive.read_xml("xl/workbook.xml")
        relationships = _relationships(archive, "xl/workbook.xml", "xl")
        archive.shared = _shared_strings(archive)
        archive.style_dates = _style_date_flags(archive)
        sheets = []
        for node in workbook.findall(f"{{{NS_SML}}}sheets/{{{NS_SML}}}sheet"):
            name = node.attrib.get("name", "").strip()
            target = relationships.get(node.attrib.get(f"{{{NS_OFFICE_REL}}}id", ""))
            if not name or not target:
                raise ValueError("unsupported_corrupt_document")
            sheets.append((name, target[1]))
        blocks = []
        total_rows = 0
        indexed_rows = 0
        indexed_sheets = 0
        truncated = len(sheets) > MAX_XLSX_SHEETS
        for index, (name, target) in enumerate(sheets[:MAX_XLSX_SHEETS]):
            if len(blocks) >= MAX_TOTAL_BLOCKS or total_rows >= MAX_TABLE_ROWS:
                truncated = True
                break
            sheet_blocks, sheet_total, sheet_indexed, sheet_truncated = _sheet_blocks(
                archive, stem, name, target, f"sheet-{index}", MAX_TOTAL_BLOCKS - len(blocks),
            )
            blocks.extend(sheet_blocks)
            total_rows += sheet_total
            indexed_rows += sheet_indexed
            if sheet_blocks:
                indexed_sheets += 1
            truncated = truncated or sheet_truncated
    _stats_add(total_rows=total_rows, indexed_rows=indexed_rows, total_sheets=len(sheets), indexed_sheets=indexed_sheets, truncated=truncated)
    return blocks, ""


def _open_csv_text(source):
    if isinstance(source, (bytes, bytearray, memoryview)):
        stream = io.BytesIO(bytes(source))
    else:
        stream = open(source, "rb")
    head = stream.read(65536)
    try:
        head.decode("utf-8-sig")
        encoding = "utf-8-sig"
    except UnicodeDecodeError:
        encoding = "gb18030"
    sample = head.decode(encoding, errors="replace")
    try:
        delimiter = csv.Sniffer().sniff(sample, delimiters=",;\t|").delimiter
    except csv.Error:
        delimiter = ","
    stream.seek(0)
    return io.TextIOWrapper(stream, encoding=encoding, newline="", errors="replace"), delimiter


def parse_csv(source, filename):
    text = None
    try:
        text, delimiter = _open_csv_text(source)
        reader = csv.reader(text, delimiter=delimiter)
        stem = os.path.splitext(os.path.basename(filename))[0] or "csv"
        headers = None
        window = []
        window_start = 2
        total = 0
        indexed = 0
        truncated = False
        blocks = []

        def flush():
            nonlocal window, blocks, truncated
            if not window:
                return
            budget = MAX_TOTAL_BLOCKS - len(blocks)
            if budget <= 0:
                truncated = True
                window = []
                return
            emitted = _emit_table_blocks("csv", stem, stem, headers, window, window_start, budget)
            blocks.extend(emitted)
            if len(blocks) >= MAX_TOTAL_BLOCKS:
                truncated = True
            window = []

        try:
            for values in reader:
                if not any(value.strip() for value in values):
                    continue
                total += 1
                if headers is None:
                    headers = _normalize_table_headers(values)
                    continue
                if indexed >= MAX_TABLE_ROWS:
                    truncated = True
                    break
                row = [_table_cell(values[index] if index < len(values) else "") for index in range(len(headers))]
                if not window:
                    window_start = 2 + indexed
                window.append(row)
                indexed += 1
                if len(window) >= MAX_TABLE_WINDOW_ROWS:
                    flush()
                    if truncated:
                        break
        except csv.Error as error:
            raise ValueError("unsupported_corrupt_document") from error
        flush()
        if headers is None:
            raise ValueError("empty_document")
        if not indexed:
            blocks = [block("csv-0", "table", " | ".join(headers), stem, 0, "csv:1:1")]
        if truncated and not any("truncated" in item.text for item in blocks):
            blocks.append(block("csv-truncated", "table", "[truncated: additional rows not indexed]", stem, 0, "csv:truncated"))
        _stats_add(total_rows=total, indexed_rows=indexed, total_sheets=1, indexed_sheets=1, truncated=truncated)
        return blocks, ""
    finally:
        if text is not None:
            text.close()


def _slide_paragraphs(tx_body):
    paragraphs = []
    for paragraph in tx_body.findall(f"{{{NS_DML}}}p"):
        text = _collapse("".join(node.text or "" for node in paragraph.iter(f"{{{NS_DML}}}t")))
        if text:
            paragraphs.append(text[:MAX_BLOCK_CHARS])
    return paragraphs


def _table_rows(shape):
    table = shape.find(f".//{{{NS_DML}}}tbl")
    if table is None:
        return []
    rows = []
    for row in table.findall(f"{{{NS_DML}}}tr"):
        cells = [_collapse("".join(node.text or "" for node in cell.iter(f"{{{NS_DML}}}t"))) for cell in row.findall(f"{{{NS_DML}}}tc")]
        text = " | ".join(value for value in cells if value)
        if text:
            rows.append(text[:MAX_BLOCK_CHARS])
    return rows


def _nested_shapes(parent, depth=0):
    for node in parent:
        if node.tag == f"{{{NS_PML}}}grpSp" and depth < 8:
            yield from _nested_shapes(node, depth + 1)
        elif node.tag in (f"{{{NS_PML}}}sp", f"{{{NS_PML}}}graphicFrame"):
            yield node


def _shape_name(shape):
    node = shape.find(f"{{{NS_PML}}}nvSpPr/{{{NS_PML}}}cNvPr")
    return node.attrib.get("name", "") if node is not None else ""


def _layout_title_placeholders(archive, slide_part):
    directory, name = posixpath.split(slide_part)
    rels_name = posixpath.join(directory, "_rels", name + ".rels")
    if not archive.has(rels_name):
        return set()
    root = archive.read_xml(rels_name)
    layout_part = ""
    for rel in root.findall(f"{{{NS_PKG_REL}}}Relationship"):
        if rel.attrib.get("Type", "").endswith("/slideLayout"):
            layout_part = _resolve_target(directory, rel.attrib.get("Target", ""))
            break
    if not layout_part or not archive.has(layout_part):
        return set()
    layout = archive.read_xml(layout_part)
    indices = set()
    for placeholder in layout.iter(f"{{{NS_PML}}}ph"):
        if placeholder.attrib.get("type", "") in ("title", "ctrTitle"):
            index = placeholder.attrib.get("idx", "")
            if index:
                indices.add(index)
    return indices


def _is_title_shape(shape, layout_titles):
    placeholder = shape.find(f"{{{NS_PML}}}nvSpPr/{{{NS_PML}}}nvPr/{{{NS_PML}}}ph")
    if placeholder is not None:
        if placeholder.attrib.get("type", "") in ("title", "ctrTitle"):
            return True
        index = placeholder.attrib.get("idx", "")
        return bool(layout_titles) and index in layout_titles
    return "title" in _shape_name(shape).lower()


def _slide_record(archive, part):
    root = archive.read_xml(part)
    layout_titles = _layout_title_placeholders(archive, part)
    tree = root.find(f"{{{NS_PML}}}cSld/{{{NS_PML}}}spTree")
    title_paragraphs, body_paragraphs = [], []
    if tree is not None:
        for shape in list(_nested_shapes(tree))[:MAX_SLIDE_SHAPES]:
            if shape.tag == f"{{{NS_PML}}}graphicFrame":
                body_paragraphs.extend(_table_rows(shape))
                continue
            tx_body = shape.find(f"{{{NS_PML}}}txBody")
            if tx_body is None:
                continue
            paragraphs = _slide_paragraphs(tx_body)[:MAX_SHAPE_PARAGRAPHS]
            if not paragraphs:
                continue
            if not title_paragraphs and _is_title_shape(shape, layout_titles):
                title_paragraphs = paragraphs
            else:
                body_paragraphs.extend(paragraphs)
    items = [("heading", text) for text in title_paragraphs]
    items.extend(("paragraph", text) for text in body_paragraphs)
    return (title_paragraphs[0] if title_paragraphs else ""), items


def parse_pptx(source, filename):
    stem = os.path.splitext(os.path.basename(filename))[0] or "presentation"
    with _OoxmlArchive(source) as archive:
        presentation = archive.read_xml("ppt/presentation.xml")
        relationships = _relationships(archive, "ppt/presentation.xml", "ppt")
        parts = []
        for node in presentation.findall(f"{{{NS_PML}}}sldIdLst/{{{NS_PML}}}sldId"):
            target = relationships.get(node.attrib.get(f"{{{NS_OFFICE_REL}}}id", ""))
            if not target:
                raise ValueError("unsupported_corrupt_document")
            parts.append(target[1])
        records = [(page, _slide_record(archive, part)) for page, part in enumerate(parts[:MAX_PPTX_SLIDES], 1)]
        deck_title = next((title for _, (title, _) in records if title), "")
        heading_root = deck_title or stem
        blocks = []
        for page, (title, items) in records:
            if len(blocks) >= MAX_TOTAL_BLOCKS:
                break
            heading_path = heading_root if not title or title == heading_root else f"{heading_root} / {title}"
            for kind, text in items:
                if len(blocks) >= MAX_TOTAL_BLOCKS:
                    break
                blocks.append(block(f"slide-{page}-{len(blocks)}", kind, text, heading_path, page, f"slide:{page}"))
        if len(parts) > len(records) or len(blocks) >= MAX_TOTAL_BLOCKS:
            blocks.append(block("slide-truncated", "paragraph", f"[truncated: {max(len(parts) - len(records), 1)} more slides]", heading_root, 0, "slide:truncated"))
    return blocks, deck_title


def _decode_html(content):
    if len(content) > MAX_HTML_BYTES:
        raise ValueError("document_too_complex")
    try:
        return content.decode("utf-8-sig")
    except UnicodeDecodeError:
        pass
    match = META_CHARSET.search(content[:4096])
    if match:
        try:
            return content.decode(match.group(1).decode("ascii", "ignore"), errors="replace")
        except (LookupError, UnicodeDecodeError):
            pass
    return content.decode("utf-8", errors="replace")


class _HtmlExtractor(html.parser.HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.blocks = []
        self.title = ""
        self._title_parts = []
        self._in_title = False
        self._head_depth = 0
        self._skip_tag = ""
        self._skip_depth = 0
        self._pre_depth = 0
        self._heading_stack = []
        self._heading_level = 0
        self._heading_parts = []
        self._heading_line = 0
        self._buffer = []
        self._buffer_line = 0
        self._row_cells = None
        self._cell_parts = None
        self._truncated = False

    def _emit(self, kind, text, heading_path, line, pre=False):
        if self._truncated:
            return
        text = text.strip() if pre else _collapse(text)
        if not text:
            return
        if len(self.blocks) >= MAX_TOTAL_BLOCKS:
            self._truncated = True
            return
        self.blocks.append(block(f"html-{len(self.blocks)}", kind, text[:MAX_BLOCK_CHARS], heading_path, 0, f"line:{max(line, 1)}"))

    def _append(self, data):
        if not data:
            return
        if self._in_title:
            self._title_parts.append(data)
            return
        if self._skip_tag or self._head_depth:
            return
        if self._heading_level:
            self._heading_parts.append(data)
            return
        if self._cell_parts is not None:
            self._cell_parts.append(data)
            return
        if not self._buffer:
            self._buffer_line = self.getpos()[0]
        self._buffer.append(data)

    def _flush(self):
        if self._heading_level or self._cell_parts is not None or not self._buffer:
            return
        text = "".join(self._buffer)
        line = self._buffer_line
        self._buffer = []
        self._buffer_line = 0
        self._emit("paragraph", text, " / ".join(self._heading_stack), line, pre=self._pre_depth > 0)

    def handle_starttag(self, tag, attrs):
        tag = tag.lower()
        if tag == "title":
            self._in_title = True
            return
        if self._in_title:
            return
        if self._skip_tag:
            if tag == self._skip_tag:
                self._skip_depth += 1
            return
        if tag == "head":
            self._head_depth += 1
            return
        if self._head_depth:
            return
        if tag in HTML_SKIP_TAGS:
            self._flush()
            self._skip_tag = tag
            self._skip_depth = 1
            return
        if tag in HTML_HEADING_TAGS:
            self._flush()
            self._heading_level = HTML_HEADING_TAGS[tag]
            self._heading_parts = []
            self._heading_line = self.getpos()[0]
            return
        if tag == "pre":
            self._flush()
            self._pre_depth += 1
            return
        if tag == "br":
            self._append("\n" if self._pre_depth else " ")
            return
        if tag == "tr":
            self._flush()
            self._row_cells = []
            self._cell_parts = None
            return
        if tag in ("td", "th"):
            if self._row_cells is None:
                self._row_cells = []
            self._cell_parts = []
            return
        if tag in HTML_BLOCK_TAGS:
            self._flush()

    def handle_endtag(self, tag):
        tag = tag.lower()
        if tag == "title":
            if self._in_title:
                self._in_title = False
                title = _collapse("".join(self._title_parts))
                if title and not self.title:
                    self.title = title
            return
        if self._in_title:
            return
        if self._skip_tag:
            if tag == self._skip_tag:
                self._skip_depth -= 1
                if self._skip_depth <= 0:
                    self._skip_tag = ""
                    self._skip_depth = 0
            return
        if tag == "head":
            if self._head_depth:
                self._head_depth -= 1
            return
        if self._head_depth:
            return
        if tag in HTML_HEADING_TAGS and self._heading_level == HTML_HEADING_TAGS[tag]:
            text = _collapse("".join(self._heading_parts))
            self._heading_parts = []
            level = self._heading_level
            self._heading_level = 0
            if text:
                self._heading_stack = self._heading_stack[: level - 1] + [text]
                self._emit("heading", text, " / ".join(self._heading_stack), self._heading_line)
            return
        if tag == "pre":
            if self._pre_depth:
                self._flush()
                self._pre_depth -= 1
            return
        if tag in ("td", "th"):
            if self._cell_parts is not None:
                if self._row_cells is None:
                    self._row_cells = []
                self._row_cells.append(_collapse("".join(self._cell_parts)))
                self._cell_parts = None
            return
        if tag == "tr":
            if self._row_cells is not None:
                cells = self._row_cells
                self._row_cells = None
                self._emit("paragraph", " | ".join(cell for cell in cells if cell), " / ".join(self._heading_stack), self.getpos()[0])
            self._flush()
            return
        if tag in HTML_BLOCK_TAGS:
            self._flush()

    def handle_data(self, data):
        self._append(data)

    def finish(self):
        if self._heading_level:
            text = _collapse("".join(self._heading_parts))
            self._heading_parts = []
            level = self._heading_level
            self._heading_level = 0
            if text:
                self._heading_stack = self._heading_stack[: level - 1] + [text]
                self._emit("heading", text, " / ".join(self._heading_stack), self._heading_line)
        if self._cell_parts is not None:
            if self._row_cells is None:
                self._row_cells = []
            self._row_cells.append(_collapse("".join(self._cell_parts)))
            self._cell_parts = None
        if self._row_cells is not None:
            cells = self._row_cells
            self._row_cells = None
            self._emit("paragraph", " | ".join(cell for cell in cells if cell), " / ".join(self._heading_stack), max(self._buffer_line, 1))
        self._flush()


def parse_html(source, filename):
    content = _source_bytes(source)
    extractor = _HtmlExtractor()
    extractor.feed(_decode_html(content))
    extractor.close()
    extractor.finish()
    return extractor.blocks, extractor.title


def parse_content(filename, mime_type, source):
    lowered = filename.lower()
    mime_type = (mime_type or "").lower()
    if lowered.endswith(".xls") or lowered.endswith(".ppt"):
        raise ValueError("unsupported_legacy_office")
    if lowered.endswith(".xlsx") or mime_type == SHEET_MIME:
        return parse_xlsx(source, filename)
    if lowered.endswith(".csv") or mime_type in ("text/csv", "application/csv"):
        return parse_csv(source, filename)
    if lowered.endswith(".pptx") or mime_type == SLIDE_MIME:
        return parse_pptx(source, filename)
    if lowered.endswith(".html") or lowered.endswith(".htm") or mime_type in ("text/html", "application/xhtml+xml"):
        return parse_html(source, filename)
    if lowered.endswith(".pdf") or mime_type == "application/pdf":
        return parse_pdf(source)
    if lowered.endswith(".docx") or mime_type == WORD_MIME:
        return parse_docx(source)
    if lowered.endswith(".md") or mime_type in ("text/markdown", "text/x-markdown"):
        return parse_markdown(source)
    if lowered.endswith(".txt") or mime_type.startswith("text/"):
        return parse_text(source)
    raise ValueError("unsupported_mime_type")


def _spool_reserve(size):
    global _SPOOL_USED
    with _SPOOL_LOCK:
        if _SPOOL_USED + size > SPOOL_MAX_BYTES:
            return False
        _SPOOL_USED += size
        return True


def _spool_release(size):
    global _SPOOL_USED
    with _SPOOL_LOCK:
        _SPOOL_USED = max(0, _SPOOL_USED - size)


def _log_parse(filename, started, blocks, stats, error_code=""):
    print(
        '{"level":"%s","msg":"parse_complete","filename":%s,"blocks":%d,"truncated":%s,'
        '"total_rows":%d,"indexed_rows":%d,"total_sheets":%d,"indexed_sheets":%d,"elapsed_ms":%d,"error_code":"%s"}'
        % (
            "ERROR" if error_code else "INFO", json.dumps(filename), len(blocks), str(stats["truncated"]).lower(),
            stats["total_rows"], stats["indexed_rows"], stats["total_sheets"], stats["indexed_sheets"],
            int((time.monotonic() - started) * 1000), error_code,
        ),
        flush=True,
    )


class _SpoolUpload:
    def __init__(self):
        self.meta = None
        self.spool = None
        self.spool_path = ""
        self.hasher = hashlib.sha256()
        self.reserved = 0
        self.acquired = False

    def response(self, code, message, version_id="", mime_type=""):
        return docreader_pb2.ParseDocumentResponse(
            schema_version=SCHEMA_VERSION,
            document_version_id=version_id or (self.meta[0] if self.meta else ""),
            content_hash=self.hasher.hexdigest(),
            mime_type=mime_type or (self.meta[2] if self.meta else ""),
            parser_version=PARSER_VERSION, error_code=code, error_message=message,
        )

    def close(self):
        if self.spool is not None:
            try:
                self.spool.close()
            except OSError:
                pass
            self.spool = None
        if self.spool_path:
            try:
                os.unlink(self.spool_path)
            except OSError:
                pass
            self.spool_path = ""
        if self.acquired:
            SPOOL_SEMAPHORE.release()
            self.acquired = False
        if self.reserved:
            _spool_release(self.reserved)
            self.reserved = 0


def _receive_spool_upload(request_iterator):
    state = _SpoolUpload()
    try:
        for frame in request_iterator:
            if state.meta is None:
                if frame.schema_version != "SourceObjectV1":
                    state.close()
                    return None, docreader_pb2.ParseDocumentResponse(
                        schema_version=SCHEMA_VERSION, document_version_id=frame.document_version_id,
                        error_code="schema_incompatible", error_message="unsupported source schema",
                    )
                state.meta = (frame.document_version_id, frame.filename, frame.mime_type)
            if not frame.content:
                continue
            if not SPOOL_READY:
                state.close()
                return None, state.response("spool_unavailable", "parse spool is not writable")
            state.hasher.update(frame.content)
            if state.reserved + len(frame.content) > MAX_FILE_BYTES:
                state.close()
                return None, state.response("file_too_large", "file exceeds maximum size")
            if not state.acquired:
                state.acquired = SPOOL_SEMAPHORE.acquire(timeout=SPOOL_WAIT_SECONDS)
                if not state.acquired:
                    state.close()
                    return None, state.response("parser_busy", "too many concurrent parses")
            if state.spool is None:
                fd, state.spool_path = tempfile.mkstemp(prefix="docreader-", dir=SPOOL_DIR)
                state.spool = os.fdopen(fd, "wb")
            if not _spool_reserve(len(frame.content)):
                state.close()
                return None, state.response("parser_busy", "parse spool is full")
            state.reserved += len(frame.content)
            state.spool.write(frame.content)
        if state.meta is None:
            state.close()
            return None, docreader_pb2.ParseDocumentResponse(
                schema_version=SCHEMA_VERSION, error_code="request_invalid", error_message="stream metadata is required",
            )
        if state.spool is None:
            state.close()
            return None, state.response("empty_document", "stream had no content")
        state.spool.close()
        state.spool = None
        return state, None
    except Exception:
        state.close()
        raise


class Reader(docreader_pb2_grpc.DocumentReaderServicer):
    def ParseDocument(self, request, context):
        _stats_reset()
        started = time.monotonic()
        if request.schema_version != "SourceObjectV1":
            return docreader_pb2.ParseDocumentResponse(schema_version=SCHEMA_VERSION, document_version_id=request.document_version_id, error_code="schema_incompatible", error_message="unsupported source schema")
        try:
            blocks, title = parse_content(request.filename, request.mime_type, request.content)
        except ValueError as error:
            code = str(error)
            return docreader_pb2.ParseDocumentResponse(schema_version=SCHEMA_VERSION, document_version_id=request.document_version_id, content_hash=hashlib.sha256(request.content).hexdigest(), mime_type=request.mime_type, parser_version=PARSER_VERSION, error_code=code, error_message=code)
        except Exception as error:
            return docreader_pb2.ParseDocumentResponse(schema_version=SCHEMA_VERSION, document_version_id=request.document_version_id, content_hash=hashlib.sha256(request.content).hexdigest(), mime_type=request.mime_type, parser_version=PARSER_VERSION, error_code="parser_error", error_message=str(error)[:500])
        stats = _stats_snapshot()
        _log_parse(request.filename, started, blocks, stats)
        return docreader_pb2.ParseDocumentResponse(
            schema_version=SCHEMA_VERSION, document_version_id=request.document_version_id,
            content_hash=hashlib.sha256(request.content).hexdigest(), mime_type=request.mime_type,
            title=title or request.filename, language="und", parser_version=PARSER_VERSION, blocks=blocks,
            truncated=stats["truncated"], total_rows=stats["total_rows"], indexed_rows=stats["indexed_rows"],
            total_sheets=stats["total_sheets"], indexed_sheets=stats["indexed_sheets"],
        )

    def ParseDocumentStream(self, request_iterator, context):
        _stats_reset()
        started = time.monotonic()
        state, error_response = _receive_spool_upload(request_iterator)
        if error_response is not None:
            return error_response
        try:
            meta = state.meta
            try:
                blocks, title = parse_content(meta[1], meta[2], state.spool_path)
            except ValueError as error:
                code = str(error)
                return state.response(code, code)
            except Exception as error:
                return state.response("parser_error", str(error)[:500])
            stats = _stats_snapshot()
            _log_parse(meta[1], started, blocks, stats)
            return docreader_pb2.ParseDocumentResponse(
                schema_version=SCHEMA_VERSION, document_version_id=meta[0], content_hash=state.hasher.hexdigest(),
                mime_type=meta[2], title=title or meta[1], language="und", parser_version=PARSER_VERSION, blocks=blocks,
                truncated=stats["truncated"], total_rows=stats["total_rows"], indexed_rows=stats["indexed_rows"],
                total_sheets=stats["total_sheets"], indexed_sheets=stats["indexed_sheets"],
            )
        finally:
            state.close()

    def ParseDocumentBlocks(self, request_iterator, context):
        _stats_reset()
        started = time.monotonic()
        state, error_response = _receive_spool_upload(request_iterator)
        if error_response is not None:
            yield docreader_pb2.ParseDocumentEvent(summary=error_response)
            return
        try:
            meta = state.meta
            try:
                blocks, title = parse_content(meta[1], meta[2], state.spool_path)
            except ValueError as error:
                code = str(error)
                yield docreader_pb2.ParseDocumentEvent(summary=state.response(code, code))
                return
            except Exception as error:
                yield docreader_pb2.ParseDocumentEvent(summary=state.response("parser_error", str(error)[:500]))
                return
            stats = _stats_snapshot()
            _log_parse(meta[1], started, blocks, stats)
            batch = max(1, DOCREADER_BLOCK_BATCH)
            for start in range(0, len(blocks), batch):
                yield docreader_pb2.ParseDocumentEvent(blocks=blocks[start:start + batch])
            yield docreader_pb2.ParseDocumentEvent(summary=docreader_pb2.ParseDocumentResponse(
                schema_version=SCHEMA_VERSION, document_version_id=meta[0], content_hash=state.hasher.hexdigest(),
                mime_type=meta[2], title=title or meta[1], language="und", parser_version=PARSER_VERSION,
                truncated=stats["truncated"], total_rows=stats["total_rows"], indexed_rows=stats["indexed_rows"],
                total_sheets=stats["total_sheets"], indexed_sheets=stats["indexed_sheets"],
            ))
        finally:
            state.close()


class HealthHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path in ("/healthz", "/readyz"):
            payload = b'{"status":"ok","mode":"m2-grpc","ocr":false,"spool_ok":%s,"spool_dir":"%s"}' % (
            b"true" if SPOOL_READY else b"false", SPOOL_DIR.encode("utf-8", "replace"),
        )
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
            return
        self.send_response(404)
        self.end_headers()

    def log_message(self, fmt, *args):
        print('{"level":"INFO","msg":"health_request"}', flush=True)


def serve():
    grpc_port = int(os.getenv("DOCREADER_GRPC_PORT", "50051"))
    health_port = int(os.getenv("DOCREADER_HEALTH_PORT", "8080"))
    server = grpc.server(
        futures.ThreadPoolExecutor(max_workers=4),
        options=[
            # Unary clients keep the original 128MiB envelope; streaming frames
            # are bounded by the client at 2MiB, so this does not increase the
            # streaming memory footprint.
            ("grpc.max_receive_message_length", 128 * 1024 * 1024),
            ("grpc.max_send_message_length", 128 * 1024 * 1024),
        ],
    )
    docreader_pb2_grpc.add_DocumentReaderServicer_to_server(Reader(), server)
    server.add_insecure_port(f"[::]:{grpc_port}")
    server.start()
    health = http.server.ThreadingHTTPServer(("0.0.0.0", health_port), HealthHandler)
    threading.Thread(target=health.serve_forever, daemon=True).start()
    server.wait_for_termination()


if __name__ == "__main__":
    serve()
