import hashlib
import html.parser
import http.server
import io
import os
import posixpath
import re
import sys
import threading
import zipfile
from concurrent import futures
from datetime import date, timedelta
from xml.etree import ElementTree

import grpc
from pypdf import PdfReader

sys.path.insert(0, "/app/generated")
import docreader_pb2  # type: ignore
import docreader_pb2_grpc  # type: ignore

PARSER_VERSION = os.getenv("PARSER_VERSION", "docreader-m2-v2")
SCHEMA_VERSION = "NormalizedDocumentV1"

WORD_MIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
SHEET_MIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
SLIDE_MIME = "application/vnd.openxmlformats-officedocument.presentationml.presentation"

NS_SML = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
NS_PML = "http://schemas.openxmlformats.org/presentationml/2006/main"
NS_DML = "http://schemas.openxmlformats.org/drawingml/2006/main"
NS_OFFICE_REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
NS_PKG_REL = "http://schemas.openxmlformats.org/package/2006/relationships"

MAX_ARCHIVE_ENTRIES = 2048
MAX_PART_BYTES = 32 * 1024 * 1024
MAX_ARCHIVE_BYTES = 256 * 1024 * 1024
MAX_COMPRESSION_RATIO = 1000
MAX_XML_DEPTH = 64
MAX_TOTAL_BLOCKS = 20000
MAX_BLOCK_CHARS = 8000
MAX_XLSX_SHEETS = 64
MAX_XLSX_ROWS = 5000
MAX_XLSX_COLUMNS = 128
MAX_CELL_CHARS = 4096
MAX_PPTX_SLIDES = 500
MAX_SLIDE_SHAPES = 200
MAX_SHAPE_PARAGRAPHS = 200
MAX_HTML_BYTES = 16 * 1024 * 1024

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


def parse_markdown(content):
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


def parse_text(content):
    blocks, offset = [], 0
    for index, raw in enumerate(content.decode("utf-8-sig").splitlines(keepends=True)):
        line = raw.rstrip("\r\n")
        if line.strip():
            blocks.append(block(f"block-{index}", "paragraph", line.strip(), "", 0, f"line:{index + 1}", offset, offset + len(line)))
        offset += len(raw)
    return blocks, ""


def parse_docx(content):
    with zipfile.ZipFile(io.BytesIO(content)) as archive:
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


def parse_pdf(content):
    reader = PdfReader(io.BytesIO(content))
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
    def __init__(self, content):
        try:
            self._zip = zipfile.ZipFile(io.BytesIO(content))
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


def _sheet_blocks(archive, heading_root, sheet_name, target, shared, style_dates, prefix, budget):
    root = archive.read_xml(target)
    rows = root.findall(f"{{{NS_SML}}}sheetData/{{{NS_SML}}}row")
    rendered = []
    rendered_rows = 0
    last_column = 0
    last_row = 0
    for position, row in enumerate(rows):
        if rendered_rows >= MAX_XLSX_ROWS or rendered_rows >= budget:
            break
        try:
            row_number = int(row.attrib.get("r", "0"))
        except ValueError:
            row_number = 0
        row_number = row_number or position + 1
        cells = []
        for index, cell in enumerate(row.findall(f"{{{NS_SML}}}c")):
            reference = CELL_REF.match(cell.attrib.get("r", "").upper())
            column = _column_index(reference.group(1)) if reference else index
            if column >= MAX_XLSX_COLUMNS:
                continue
            value = _cell_text(cell, shared, style_dates)
            if not value:
                continue
            cells.append((column, value[:MAX_CELL_CHARS]))
            last_column = max(last_column, column)
            last_row = max(last_row, row_number)
        if not cells:
            continue
        cells.sort()
        text = " | ".join(f"{_column_letter(column)}: {value}" for column, value in cells)
        rendered.append(text[:MAX_BLOCK_CHARS])
        rendered_rows += 1
    heading_path = f"{heading_root} / {sheet_name}"
    source_path = f"sheet:{sheet_name}!A1:{_column_letter(last_column)}{max(last_row, 1)}"
    blocks = [block(f"{prefix}-{index}", "paragraph", text, heading_path, 0, source_path) for index, text in enumerate(rendered)]
    remaining_rows = len(rows) - rendered_rows
    if remaining_rows > 0:
        blocks.append(block(f"{prefix}-truncated", "paragraph", f"[truncated: {remaining_rows} more rows]", heading_path, 0, source_path))
    return blocks


def parse_xlsx(content, filename):
    stem = os.path.splitext(os.path.basename(filename))[0] or "workbook"
    with _OoxmlArchive(content) as archive:
        workbook = archive.read_xml("xl/workbook.xml")
        relationships = _relationships(archive, "xl/workbook.xml", "xl")
        shared = _shared_strings(archive)
        style_dates = _style_date_flags(archive)
        sheets = []
        for node in workbook.findall(f"{{{NS_SML}}}sheets/{{{NS_SML}}}sheet"):
            name = node.attrib.get("name", "").strip()
            target = relationships.get(node.attrib.get(f"{{{NS_OFFICE_REL}}}id", ""))
            if not name or not target:
                raise ValueError("unsupported_corrupt_document")
            sheets.append((name, target[1]))
        blocks = []
        rendered = 0
        for index, (name, target) in enumerate(sheets):
            if rendered >= MAX_XLSX_SHEETS or len(blocks) >= MAX_TOTAL_BLOCKS:
                blocks.append(block("sheet-truncated", "paragraph", f"[truncated: {len(sheets) - index} more sheets]", stem, 0, "sheet:truncated"))
                break
            blocks.extend(_sheet_blocks(archive, stem, name, target, shared, style_dates, f"sheet-{index}", MAX_TOTAL_BLOCKS - len(blocks)))
            rendered += 1
    return blocks, ""


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


def parse_pptx(content, filename):
    stem = os.path.splitext(os.path.basename(filename))[0] or "presentation"
    with _OoxmlArchive(content) as archive:
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


def parse_html(content, filename):
    extractor = _HtmlExtractor()
    extractor.feed(_decode_html(content))
    extractor.close()
    extractor.finish()
    return extractor.blocks, extractor.title


def parse_content(filename, mime_type, content):
    lowered = filename.lower()
    mime_type = (mime_type or "").lower()
    if lowered.endswith(".xls") or lowered.endswith(".ppt"):
        raise ValueError("unsupported_legacy_office")
    if lowered.endswith(".xlsx") or mime_type == SHEET_MIME:
        return parse_xlsx(content, filename)
    if lowered.endswith(".pptx") or mime_type == SLIDE_MIME:
        return parse_pptx(content, filename)
    if lowered.endswith(".html") or lowered.endswith(".htm") or mime_type in ("text/html", "application/xhtml+xml"):
        return parse_html(content, filename)
    if lowered.endswith(".pdf") or mime_type == "application/pdf":
        return parse_pdf(content)
    if lowered.endswith(".docx") or mime_type == WORD_MIME:
        return parse_docx(content)
    if lowered.endswith(".md") or mime_type in ("text/markdown", "text/x-markdown"):
        return parse_markdown(content)
    if lowered.endswith(".txt") or mime_type.startswith("text/"):
        return parse_text(content)
    raise ValueError("unsupported_mime_type")


class Reader(docreader_pb2_grpc.DocumentReaderServicer):
    def ParseDocument(self, request, context):
        if request.schema_version != "SourceObjectV1":
            return docreader_pb2.ParseDocumentResponse(schema_version=SCHEMA_VERSION, document_version_id=request.document_version_id, error_code="schema_incompatible", error_message="unsupported source schema")
        try:
            blocks, title = parse_content(request.filename, request.mime_type, request.content)
        except ValueError as error:
            code = str(error)
            return docreader_pb2.ParseDocumentResponse(schema_version=SCHEMA_VERSION, document_version_id=request.document_version_id, content_hash=hashlib.sha256(request.content).hexdigest(), mime_type=request.mime_type, parser_version=PARSER_VERSION, error_code=code, error_message=code)
        except Exception as error:
            return docreader_pb2.ParseDocumentResponse(schema_version=SCHEMA_VERSION, document_version_id=request.document_version_id, content_hash=hashlib.sha256(request.content).hexdigest(), mime_type=request.mime_type, parser_version=PARSER_VERSION, error_code="parser_error", error_message=str(error)[:500])
        return docreader_pb2.ParseDocumentResponse(schema_version=SCHEMA_VERSION, document_version_id=request.document_version_id, content_hash=hashlib.sha256(request.content).hexdigest(), mime_type=request.mime_type, title=title or request.filename, language="und", parser_version=PARSER_VERSION, blocks=blocks)


class HealthHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path in ("/healthz", "/readyz"):
            payload = b'{"status":"ok","mode":"m2-grpc","ocr":false}'
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
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=4), options=[("grpc.max_receive_message_length", 128 * 1024 * 1024)])
    docreader_pb2_grpc.add_DocumentReaderServicer_to_server(Reader(), server)
    server.add_insecure_port(f"[::]:{grpc_port}")
    server.start()
    health = http.server.ThreadingHTTPServer(("0.0.0.0", health_port), HealthHandler)
    threading.Thread(target=health.serve_forever, daemon=True).start()
    server.wait_for_termination()


if __name__ == "__main__":
    serve()
