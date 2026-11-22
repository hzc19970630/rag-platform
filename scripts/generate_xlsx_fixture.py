#!/usr/bin/env python3
"""Generate a dependency-free multi-sheet XLSX fixture for B38 acceptance."""
import argparse
import html
import os
import zipfile

CONTENT_TYPES = """<?xml version="1.0" encoding="UTF-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
</Types>"""

ROOT_RELS = """<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>"""


def cell(ref, value):
    return '<c r="%s" t="inlineStr"><is><t>%s</t></is></c>' % (ref, html.escape(str(value)))


def sheet_xml(rows):
    parts = ['<?xml version="1.0" encoding="UTF-8"?>',
             '<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">',
             '<dimension ref="A1:B%d"/>' % max(len(rows), 1), '<sheetData>']
    for index, row in enumerate(rows, start=1):
        parts.append('<row r="%d">%s%s</row>' % (index, cell("A%d" % index, row[0]), cell("B%d" % index, row[1])))
    parts.append('</sheetData></worksheet>')
    return "".join(parts)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--out", required=True)
    parser.add_argument("--sheets", type=int, default=65)
    parser.add_argument("--rows", type=int, default=3)
    parser.add_argument("--large-sheet-rows", type=int, default=0)
    args = parser.parse_args()

    sheet_names = ["Sheet%d" % index for index in range(1, args.sheets + 1)]
    if args.large_sheet_rows:
        sheet_names[-1] = "Huge"
    workbook = ['<?xml version="1.0" encoding="UTF-8"?>',
                '<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" '
                'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>']
    rels = ['<?xml version="1.0" encoding="UTF-8"?>',
            '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">']
    for index, name in enumerate(sheet_names, start=1):
        workbook.append('<sheet name="%s" sheetId="%d" r:id="rId%d"/>' % (html.escape(name), index, index))
        rels.append('<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>' % (index, index))
    workbook.append('</sheets></workbook>')
    rels.append('</Relationships>')

    with zipfile.ZipFile(args.out, "w", zipfile.ZIP_DEFLATED) as archive:
        archive.writestr("[Content_Types].xml", CONTENT_TYPES)
        archive.writestr("_rels/.rels", ROOT_RELS)
        archive.writestr("xl/workbook.xml", "".join(workbook))
        archive.writestr("xl/_rels/workbook.xml.rels", "".join(rels))
        for index, name in enumerate(sheet_names, start=1):
            row_count = args.large_sheet_rows if name == "Huge" else args.rows
            rows = [("name", "value")]
            rows.extend(("%s-row-%d" % (name, row), str(row)) for row in range(1, row_count + 1))
            archive.writestr("xl/worksheets/sheet%d.xml" % index, sheet_xml(rows))

    print(os.path.getsize(args.out))


if __name__ == "__main__":
    main()
