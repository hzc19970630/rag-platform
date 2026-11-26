#!/usr/bin/env python3
"""Regression probe for B40 docx/pptx table extraction.

Runs without grpc/pypdf/generated stubs by registering minimal stand-ins, so it
works on a developer host that has no docreader image. It exercises the real
parse_docx/parse_pptx code paths: table blocks, SheetMeta and row batches.
"""

import importlib.util
import io
import os
import sys
import types
import zipfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


class Msg:
    def __init__(self, **kwargs):
        self.__dict__.update(kwargs)


def load_docreader():
    pb2 = types.ModuleType("docreader_pb2")
    for name in (
        "DocumentBlock", "TableMeta", "SheetMeta", "TableRowBatch", "TableRow",
        "ParseDocumentResponse", "ParseDocumentEvent",
    ):
        setattr(pb2, name, Msg)
    pb2_grpc = types.ModuleType("docreader_pb2_grpc")
    pb2_grpc.DocumentReaderServicer = object
    pypdf = types.ModuleType("pypdf")
    pypdf.PdfReader = object
    grpc = types.ModuleType("grpc")
    grpc.server = lambda *args, **kwargs: None
    grpc.insecure_server = lambda *args, **kwargs: None
    grpc.max_receive_message_length = object()
    sys.modules.update({
        "docreader_pb2": pb2, "docreader_pb2_grpc": pb2_grpc,
        "pypdf": pypdf, "grpc": grpc,
    })
    spec = importlib.util.spec_from_file_location(
        "docreader_main", os.path.join(ROOT, "services", "docreader", "main.py"),
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def zip_bytes(parts):
    buffer = io.BytesIO()
    with zipfile.ZipFile(buffer, "w", zipfile.ZIP_DEFLATED) as archive:
        for name, content in parts.items():
            archive.writestr(name, content)
    return buffer.getvalue()


DOCX = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"
  xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
  xmlns:c="http://schemas.openxmlformats.org/drawingml/2006/chart"
  xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <w:body>
    <w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>客户表</w:t></w:r></w:p>
    <w:p><w:r><w:t>这是说明</w:t></w:r></w:p>
    <w:p><w:r><w:drawing><a:graphic><a:graphicData><a:blip r:embed="rId1"/></a:graphicData></a:graphic></w:drawing></w:r></w:p>
    <w:p><w:r><w:drawing><a:graphic><a:graphicData><c:chart r:id="rId2"/></a:graphicData></a:graphic></w:drawing></w:r></w:p>
    <w:tbl>
      <w:tr>
        <w:tc><w:tcPr><w:gridSpan w:val="2"/></w:tcPr><w:p><w:r><w:t>客户</w:t></w:r></w:p></w:tc>
        <w:tc><w:p><w:r><w:t>金额</w:t></w:r></w:p></w:tc>
      </w:tr>
      <w:tr>
        <w:tc><w:p><w:r><w:t>A公司</w:t></w:r></w:p></w:tc>
        <w:tc><w:p><w:r><w:t>华东</w:t></w:r></w:p></w:tc>
        <w:tc><w:p><w:r><w:t>100</w:t></w:r></w:p></w:tc>
      </w:tr>
      <w:tr>
        <w:tc><w:tcPr><w:vMerge w:val="continue"/></w:tcPr><w:p/></w:tc>
        <w:tc><w:p><w:r><w:t>华南</w:t></w:r></w:p></w:tc>
        <w:tc><w:p><w:r><w:t>200</w:t></w:r></w:p></w:tc>
      </w:tr>
    </w:tbl>
    <w:p><w:r><w:t>结尾</w:t></w:r></w:p>
  </w:body>
</w:document>
"""

FOOTNOTES = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:footnotes xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:footnote w:id="-1"><w:p/></w:footnote>
  <w:footnote w:id="1"><w:p><w:r><w:t>注1</w:t></w:r></w:p></w:footnote>
</w:footnotes>
"""

PRESENTATION = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"
  xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:sldIdLst><p:sldId id="256" r:id="rId1"/></p:sldIdLst>
</p:presentation>
"""

PRESENTATION_RELS = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>
</Relationships>
"""

SLIDE = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"
  xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
  xmlns:c="http://schemas.openxmlformats.org/drawingml/2006/chart">
  <p:cSld><p:spTree>
    <p:pic/>
    <p:graphicFrame>
      <p:nvGraphicFramePr><p:cNvPr id="5" name="Chart 1"/><p:cNvGraphicFramePr/><p:nvPr/></p:nvGraphicFramePr>
      <p:xfrm><a:off x="0" y="0"/><a:ext cx="10" cy="10"/></p:xfrm>
      <a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/chart"><c:chart/></a:graphicData></a:graphic>
    </p:graphicFrame>
    <p:sp>
      <p:nvSpPr><p:cNvPr id="2" name="Title 1"/><p:cNvSpPr/><p:nvPr><p:ph type="title"/></p:nvPr></p:nvSpPr>
      <p:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:t>季度销售</a:t></a:r></a:p></p:txBody>
    </p:sp>
    <p:sp>
      <p:nvSpPr><p:cNvPr id="3" name="Body 2"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
      <p:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:t>下面是明细</a:t></a:r></a:p></p:txBody>
    </p:sp>
    <p:graphicFrame>
      <p:nvGraphicFramePr><p:cNvPr id="4" name="Table 1"/><p:cNvGraphicFramePr/><p:nvPr/></p:nvGraphicFramePr>
      <p:xfrm><a:off x="0" y="0"/><a:ext cx="100" cy="100"/></p:xfrm>
      <a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/table">
        <a:tbl>
          <a:tblGrid><a:gridCol w="10"/><a:gridCol w="10"/></a:tblGrid>
          <a:tr>
            <a:tc><a:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:t>区域</a:t></a:r></a:p></a:txBody><a:tcPr/></a:tc>
            <a:tc><a:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:t>金额</a:t></a:r></a:p></a:txBody><a:tcPr/></a:tc>
          </a:tr>
          <a:tr>
            <a:tc><a:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:t>华东</a:t></a:r></a:p></a:txBody><a:tcPr/></a:tc>
            <a:tc><a:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:t>100</a:t></a:r></a:p></a:txBody><a:tcPr/></a:tc>
          </a:tr>
          <a:tr>
            <a:tc><a:tcPr vMerge="1"/><a:txBody><a:bodyPr/><a:lstStyle/><a:p/></a:txBody></a:tc>
            <a:tc><a:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:t>200</a:t></a:r></a:p></a:txBody><a:tcPr/></a:tc>
          </a:tr>
        </a:tbl>
      </a:graphicData></a:graphic>
    </p:graphicFrame>
  </p:spTree></p:cSld>
</p:sld>
"""


def check_docx(module):
    module._stats_reset()
    batches = []
    blocks, title = module.parse_content(
        "客户.docx",
        module.WORD_MIME,
        zip_bytes({"word/document.xml": DOCX, "word/footnotes.xml": FOOTNOTES}),
        row_sink=batches.append,
    )
    stats = module._stats_snapshot()
    assert stats["skipped_images"] == 1, stats
    assert stats["skipped_charts"] == 1, stats
    assert stats["skipped_notes"] == 1, stats
    assert title == "客户表", title
    assert [item.kind for item in blocks] == ["heading", "paragraph", "table", "paragraph"]
    table = blocks[2]
    assert table.table.sheet == "表格 1"
    assert table.text == "客户 | 客户_2 | 金额\nA公司 | 华东 | 100\nA公司 | 华南 | 200"
    assert stats["total_sheets"] == 1 and stats["indexed_sheets"] == 1
    sheet = stats["sheets"][0]
    assert sheet.header == ["客户", "客户_2", "金额"], sheet.header
    assert sheet.parsed_rows == 2 and sheet.declared_rows == 3
    assert batches[0]["sheet"] == "表格 1"
    assert batches[0]["rows"] == [["A公司", "华东", "100"], ["A公司", "华南", "200"]]
    assert batches[0]["sheet_done"] is True


def check_pptx(module):
    module._stats_reset()
    batches = []
    blocks, title = module.parse_content(
        "销售.pptx",
        module.SLIDE_MIME,
        zip_bytes({
            "ppt/presentation.xml": PRESENTATION,
            "ppt/_rels/presentation.xml.rels": PRESENTATION_RELS,
            "ppt/slides/slide1.xml": SLIDE,
        }),
        row_sink=batches.append,
    )
    stats = module._stats_snapshot()
    assert stats["skipped_images"] == 1, stats
    assert stats["skipped_charts"] == 1, stats
    assert title == "季度销售", title
    assert [item.kind for item in blocks] == ["heading", "paragraph", "table"]
    assert blocks[-1].table.sheet == "第1页 表格1"
    assert blocks[-1].text == "区域 | 金额\n华东 | 100\n华东 | 200"
    sheet = stats["sheets"][0]
    assert sheet.header == ["区域", "金额"] and sheet.parsed_rows == 2
    assert batches[0]["rows"] == [["华东", "100"], ["华东", "200"]]


def main():
    module = load_docreader()
    check_docx(module)
    check_pptx(module)
    print("b40 docreader tables: ok")


if __name__ == "__main__":
    main()