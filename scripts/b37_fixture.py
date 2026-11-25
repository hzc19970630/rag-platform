#!/usr/bin/env python3
"""Generate the B37 table-QA fixture and its expected-answer question set.

The workbook is dependency-free: two sheets with deterministic rows so the
aggregate answers in cases.json can be recomputed by eye.

  订单  A-%04d | 客户 | 数量 | 金额 | 状态    (300 rows, 金额 = row * 2)
  库存  商品-%04d | 仓库 | 库存量 | 单价       (200 rows, 库存量 = row)
"""
import argparse
import html
import json
import os
import zipfile
from pathlib import Path

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


def cell(reference, value):
    return '<c r="%s" t="inlineStr"><is><t>%s</t></is></c>' % (reference, html.escape(str(value)))


def sheet_xml(rows):
    columns = "ABCDE"[: len(rows[0])]
    parts = [
        '<?xml version="1.0" encoding="UTF-8"?>',
        '<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">',
        '<dimension ref="A1:%s%d"/>' % (columns[-1], max(len(rows), 1)),
        "<sheetData>",
    ]
    for row_index, row in enumerate(rows, start=1):
        cells = "".join(cell("%s%d" % (columns[column], row_index), value) for column, value in enumerate(row))
        parts.append('<row r="%d">%s</row>' % (row_index, cells))
    parts.append("</sheetData></worksheet>")
    return "".join(parts)


def order_rows():
    customers = ["张三", "李四", "王五", "赵六"]
    statuses = ["已完成", "待发货", "已取消"]
    rows = [("订单号", "客户", "数量", "金额", "状态")]
    for index in range(1, 301):
        rows.append((
            "A-%04d" % index,
            customers[(index - 1) % len(customers)],
            (index % 10) + 1,
            index * 2,
            statuses[(index - 1) % len(statuses)],
        ))
    return rows


def inventory_rows():
    warehouses = ["北仓", "南仓"]
    rows = [("商品", "仓库", "库存量", "单价")]
    for index in range(1, 201):
        rows.append((
            "商品-%04d" % index,
            warehouses[(index - 1) % len(warehouses)],
            index,
            (index % 50) + 1,
        ))
    return rows


def write_workbook(path):
    sheets = [("订单", order_rows()), ("库存", inventory_rows())]
    workbook = [
        '<?xml version="1.0" encoding="UTF-8"?>',
        '<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" '
        'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>',
    ]
    rels = [
        '<?xml version="1.0" encoding="UTF-8"?>',
        '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">',
    ]
    for index, (name, _) in enumerate(sheets, start=1):
        workbook.append('<sheet name="%s" sheetId="%d" r:id="rId%d"/>' % (html.escape(name), index, index))
        rels.append(
            '<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>'
            % (index, index)
        )
    workbook.append("</sheets></workbook>")
    rels.append("</Relationships>")
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as archive:
        archive.writestr("[Content_Types].xml", CONTENT_TYPES)
        archive.writestr("_rels/.rels", ROOT_RELS)
        archive.writestr("xl/workbook.xml", "".join(workbook))
        archive.writestr("xl/_rels/workbook.xml.rels", "".join(rels))
        for index, (_, rows) in enumerate(sheets, start=1):
            archive.writestr("xl/worksheets/sheet%d.xml" % index, sheet_xml(rows))


def cases_document():
    return {
        "fixture": {"sheets": [{"name": "订单", "rows": 300}, {"name": "库存", "rows": 200}]},
        "cases": [
            {"id": "count", "question": "订单一共有多少行", "route": "aggregate", "operation": "count",
             "expect": {"value": 300, "column": None}},
            {"id": "sum", "question": "订单的数量总和是多少", "route": "aggregate", "operation": "sum",
             "expect": {"value": 1650, "column": "数量"}},
            {"id": "avg", "question": "订单的平均金额是多少", "route": "aggregate", "operation": "avg",
             "expect": {"value": 301, "column": "金额"}},
            {"id": "max", "question": "订单最高的金额是多少", "route": "aggregate", "operation": "max",
             "expect": {"value": 600, "column": "金额"}},
            {"id": "top-desc", "question": "订单金额最高的前3条", "route": "aggregate", "operation": "top",
             "expect": {"keys": ["A-0300", "A-0299", "A-0298"], "keyColumn": "订单号"}},
            {"id": "top-asc", "question": "库存量最低的3个商品", "route": "aggregate", "operation": "top",
             "expect": {"keys": ["商品-0001", "商品-0002", "商品-0003"], "keyColumn": "商品"}},
            {"id": "lookup-order", "question": "订单 A-0100 的金额是多少", "route": "lookup", "operation": "rows",
             "expect": {"key": "A-0100", "rowNumber": 101, "cells": {"金额": "200"}}},
            {"id": "lookup-inventory", "question": "商品-0150 在哪个仓库", "route": "lookup", "operation": "rows",
             "expect": {"key": "商品-0150", "rowNumber": 151, "cells": {"仓库": "南仓"}}},
            {"id": "explain-order", "question": "订单这张表有哪些列", "route": "explain", "operation": None,
             "expect": {"header": ["订单号", "客户", "数量", "金额", "状态"]}},
            {"id": "explain-inventory", "question": "库存表是什么", "route": "explain", "operation": None,
             "expect": {"header": ["商品", "仓库", "库存量", "单价"]}},
        ],
        "search": [
            {"id": "search-order-row", "question": "A-0100 金额", "key": "A-0100"},
            {"id": "search-inventory-row", "question": "商品-0150 仓库", "key": "商品-0150"},
            {"id": "search-order-last", "question": "A-0300 金额", "key": "A-0300"},
            {"id": "search-customer", "question": "张三 订单", "key": "张三"},
        ],
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--out", required=True, help="XLSX output path")
    parser.add_argument("--cases", help="optional cases.json output path")
    args = parser.parse_args()
    Path(args.out).parent.mkdir(parents=True, exist_ok=True)
    write_workbook(args.out)
    if args.cases:
        Path(args.cases).parent.mkdir(parents=True, exist_ok=True)
        with open(args.cases, "w", encoding="utf-8") as handle:
            json.dump(cases_document(), handle, ensure_ascii=False, indent=2)
            handle.write("\n")
    print(os.path.getsize(args.out))


if __name__ == "__main__":
    main()
