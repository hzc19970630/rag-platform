package ingestion

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

const (
	IndexRoleBoth    = "both"
	IndexRoleKeyword = "keyword"
	IndexRoleVector  = "vector"

	IndexModeStandard = "standard"
	IndexModeDeep     = "deep"
	IndexModeLexical  = "lexical"

	StorageModeFull     = "full"
	StorageModeSampled  = "sampled"
	StorageModeMetadata = "metadata"

	ParseStatusParsed    = "parsed"
	ParseStatusTruncated = "truncated"
	ParseStatusSampled   = "sampled"
	ParseStatusMetadata  = "metadata"
	ParseStatusSkipped   = "skipped"
	ParseStatusFailed    = "failed"
)

const (
	smallTableMaxRows     = 2000
	smallTableMaxColumns  = 50
	mediumTableMaxRows    = 50000
	mediumTableMaxColumns = 100
	maxTableSheets        = 20
	sampleRowsPerTable    = 20
	charsPerTokenEstimate = 2
	maxPersistedTableRows = 200000
)

// tableDocProfile is the aggregated shape used to pick a default document mode.
type tableDocProfile struct {
	rows    int
	sheets  map[string]bool
	maxCols int
}

type tableMetaInfo struct {
	sheet       string
	header      []string
	rowStart    int
	rowEnd      int
	rowCount    int
	columnCount int
	totalRows   int64
	parsedRows  int64
	samples     [][]string
	rows        [][]string
	storageMode string
	parseStatus string
	truncated   bool
	headerRow   int
	sourcePath  string
}

func tableMetaFor(block DocumentBlock) (tableMetaInfo, bool) {
	if block.Kind != "table" {
		return tableMetaInfo{}, false
	}
	lines := cleanTableLines(block.Text)
	if len(lines) == 0 {
		return tableMetaInfo{}, false
	}
	info := tableMetaInfo{storageMode: StorageModeFull, parseStatus: ParseStatusParsed, sourcePath: block.SourcePath}
	if block.Table != nil {
		info.sheet = block.Table.Sheet
		info.header = append([]string(nil), block.Table.Header...)
		info.rowStart = block.Table.RowStart
		info.rowEnd = block.Table.RowEnd
		info.columnCount = block.Table.ColumnCount
	}
	if len(info.header) == 0 {
		info.header = splitTableLine(lines[0])
	}
	if info.columnCount == 0 {
		info.columnCount = len(info.header)
	}
	if info.sheet == "" {
		info.sheet = sheetFromSourcePath(block.SourcePath)
	}
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, "[sheet metadata:") {
			continue
		}
		if row := splitTableLine(line); len(row) > 0 {
			info.rows = append(info.rows, row)
			if len(info.samples) < sampleRowsPerTable {
				info.samples = append(info.samples, row)
			}
		}
	}
	info.rowCount = len(info.rows)
	info.totalRows = int64(info.rowCount)
	info.parsedRows = int64(info.rowCount)
	if info.rowEnd == 0 {
		info.rowEnd = info.rowStart + info.rowCount - 1
	}
	return info, true
}

func sheetMetaInfo(meta SheetMeta) tableMetaInfo {
	info := tableMetaInfo{
		sheet: meta.Name, header: append([]string(nil), meta.Header...), columnCount: meta.ColumnCount,
		totalRows: meta.TotalRows, parsedRows: meta.ParsedRows, rowCount: int(meta.ParsedRows),
		storageMode: meta.StoredStorageMode(), parseStatus: meta.ParseStatus, truncated: meta.Truncated,
		headerRow: meta.HeaderRow, sourcePath: meta.SourcePath,
	}
	if info.totalRows == 0 {
		info.totalRows = meta.DeclaredRows
	}
	if info.totalRows == 0 {
		info.totalRows = info.parsedRows
	}
	if info.storageMode == "" {
		info.storageMode = StorageModeFull
	}
	if info.parseStatus == "" {
		info.parseStatus = ParseStatusParsed
	}
	if info.headerRow == 0 && info.rowCount > 0 {
		info.headerRow = max(info.rowStart-1, 0)
	}
	info.rowStart = max(info.headerRow+1, 1)
	info.rowEnd = info.rowStart + info.rowCount - 1
	for _, line := range meta.SampleRows {
		if row := splitTableLine(line); len(row) > 0 && len(info.samples) < sampleRowsPerTable {
			info.samples = append(info.samples, row)
		}
	}
	return info
}

// StoredStorageMode preserves the parser tier even for a truncated sheet.
func (m SheetMeta) StoredStorageMode() string {
	if m.ParseStatus == ParseStatusMetadata {
		return StorageModeMetadata
	}
	if m.ParseStatus == ParseStatusSampled || m.ParseStatus == ParseStatusTruncated || m.ParseStatus == ParseStatusSkipped {
		return StorageModeSampled
	}
	return StorageModeFull
}

func splitTableLine(line string) []string {
	parts := strings.Split(line, "|")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func sheetFromSourcePath(path string) string {
	if !strings.HasPrefix(path, "sheet:") {
		return ""
	}
	value := strings.TrimPrefix(path, "sheet:")
	if separator := strings.Index(value, "!"); separator >= 0 {
		value = value[:separator]
	}
	return value
}

func tableProfile(blocks []DocumentBlock) tableDocProfile {
	profile := tableDocProfile{sheets: map[string]bool{}}
	for _, block := range blocks {
		info, ok := tableMetaFor(block)
		if !ok {
			continue
		}
		profile.rows += info.rowCount
		if info.sheet != "" {
			profile.sheets[info.sheet] = true
		}
		if info.columnCount > profile.maxCols {
			profile.maxCols = info.columnCount
		}
	}
	return profile
}

// resolveIndexMode applies the product tier: small tables default to full row
// vectorization, medium tables to summaries, large tables to lexical only.
func resolveIndexMode(documentMode string, blocks []DocumentBlock) string {
	switch documentMode {
	case IndexModeStandard, IndexModeDeep, IndexModeLexical:
		return documentMode
	}
	return modeForProfile(tableProfile(blocks))
}

func modeForProfile(profile tableDocProfile) string {
	if len(profile.sheets) == 0 {
		return IndexModeStandard
	}
	if profile.rows <= smallTableMaxRows && profile.maxCols <= smallTableMaxColumns {
		return IndexModeDeep
	}
	if profile.rows <= mediumTableMaxRows && profile.maxCols <= mediumTableMaxColumns && len(profile.sheets) <= maxTableSheets {
		return IndexModeStandard
	}
	return IndexModeLexical
}

// resolveSheetIndexMode keeps an explicit document mode as the override, but
// otherwise chooses per sheet. A multi-sheet document must not be poisoned by
// one oversized sheet.
func resolveSheetIndexMode(documentMode string, asset models.TableAsset, profile tableDocProfile) string {
	switch documentMode {
	case IndexModeStandard, IndexModeDeep, IndexModeLexical:
		return documentMode
	}
	if asset.StorageMode != "" && asset.StorageMode != StorageModeFull {
		return IndexModeStandard
	}
	rows := int(asset.TotalRows)
	if rows == 0 {
		rows = int(asset.RowCount)
	}
	if rows <= smallTableMaxRows && asset.ColumnCount <= smallTableMaxColumns && profile.rows <= smallTableMaxRows {
		return IndexModeDeep
	}
	if rows <= mediumTableMaxRows && asset.ColumnCount <= mediumTableMaxColumns {
		return IndexModeStandard
	}
	return IndexModeLexical
}

func tableSummaryText(info tableMetaInfo) string {
	var builder strings.Builder
	if info.sheet != "" {
		builder.WriteString("表: " + info.sheet + "\n")
	}
	builder.WriteString("列: " + strings.Join(info.header, ", ") + "\n")
	fmt.Fprintf(&builder, "行数: %d，列数: %d\n", info.totalRows, info.columnCount)
	if info.storageMode != "" && info.storageMode != StorageModeFull {
		fmt.Fprintf(&builder, "存储: %s，已解析 %d 行\n", info.storageMode, info.parsedRows)
	}
	if len(info.samples) > 0 {
		builder.WriteString("样本行:\n")
		for _, row := range info.samples {
			builder.WriteString(strings.Join(row, " | ") + "\n")
		}
	}
	return strings.TrimSpace(builder.String())
}

// tableAssetSummary is used for sampled/metadata sheets that have no row block
// after the parser has stopped at the catalog/sample budget.
func tableAssetSummary(asset models.TableAsset) string {
	var header, samples []string
	_ = json.Unmarshal([]byte(asset.Header), &header)
	_ = json.Unmarshal([]byte(asset.SampleRows), &samples)
	var builder strings.Builder
	builder.WriteString("表: " + asset.Sheet + "\n")
	builder.WriteString("列: " + strings.Join(header, ", ") + "\n")
	fmt.Fprintf(&builder, "行数: %d，列数: %d\n", asset.TotalRows, asset.ColumnCount)
	if asset.StorageMode != "" && asset.StorageMode != StorageModeFull {
		fmt.Fprintf(&builder, "存储: %s，已解析 %d 行\n", asset.StorageMode, asset.ParsedRows)
	}
	if len(samples) > 0 {
		builder.WriteString("样本行:\n")
		for _, line := range samples {
			builder.WriteString(line + "\n")
		}
	}
	return strings.TrimSpace(builder.String())
}

func tableAssetRow(version models.DocumentVersion, blockIndex int, info tableMetaInfo, mode string) models.TableAsset {
	header, _ := json.Marshal(info.header)
	columns, _ := json.Marshal(info.header)
	samples, _ := json.Marshal(info.samples)
	stats, _ := json.Marshal(map[string]any{"rows": info.rowCount, "columns": info.columnCount})
	return models.TableAsset{
		ID: stableID("tbl", version.ID, fmt.Sprint(blockIndex)), TenantID: version.TenantID,
		KnowledgeBaseID: version.KnowledgeBaseID, DocumentID: version.DocumentID, DocumentVersionID: version.ID,
		Sheet: info.sheet, SourcePath: "", Header: string(header), Columns: string(columns),
		RowStart: info.rowStart, RowEnd: info.rowEnd, RowCount: info.rowCount, ColumnCount: info.columnCount,
		SampleRows: string(samples), Stats: string(stats), IndexMode: mode,
		StorageMode: info.storageMode, ParseStatus: info.parseStatus, Truncated: info.truncated,
		TotalRows: info.totalRows, ParsedRows: info.parsedRows, HeaderRow: info.headerRow,
	}
}

func buildTableAssets(version models.DocumentVersion, blocks []DocumentBlock, sheets []SheetMeta) (map[int]string, []models.TableAsset) {
	type group struct {
		asset      models.TableAsset
		rows       int
		samples    [][]string
		rowStart   int
		rowEnd     int
		firstIndex int
	}
	groups := map[string]*group{}
	order := make([]string, 0)
	blockAsset := map[int]string{}
	ensure := func(sheet string) *group {
		if current, ok := groups[sheet]; ok {
			return current
		}
		current := &group{asset: models.TableAsset{
			ID: stableID("tbl", version.ID, sheet), TenantID: version.TenantID, KnowledgeBaseID: version.KnowledgeBaseID,
			DocumentID: version.DocumentID, DocumentVersionID: version.ID, Sheet: sheet,
			IndexMode: version.IndexMode, StorageMode: StorageModeFull, ParseStatus: ParseStatusParsed,
		}}
		groups[sheet] = current
		order = append(order, sheet)
		return current
	}
	for _, meta := range sheets {
		sheet := meta.Name
		if sheet == "" {
			sheet = "default"
		}
		current := ensure(sheet)
		info := sheetMetaInfo(meta)
		current.asset.Header = jsonOrEmpty(info.header)
		current.asset.Columns = jsonOrEmpty(info.header)
		current.asset.ColumnCount = info.columnCount
		current.asset.RowStart = info.rowStart
		current.asset.RowEnd = info.rowEnd
		current.asset.RowCount = int(info.parsedRows)
		current.asset.TotalRows = info.totalRows
		current.asset.ParsedRows = info.parsedRows
		current.asset.StorageMode = info.storageMode
		current.asset.ParseStatus = info.parseStatus
		current.asset.Truncated = info.truncated
		current.asset.HeaderRow = info.headerRow
		current.asset.SampleRows = jsonOrEmpty(sampleLines(info.samples))
		current.samples = info.samples
	}
	for index, block := range blocks {
		info, ok := tableMetaFor(block)
		if !ok {
			continue
		}
		sheet := info.sheet
		if sheet == "" {
			sheet = "default"
		}
		current := ensure(sheet)
		if current.firstIndex == 0 {
			current.firstIndex = index
		}
		if len(current.asset.Header) == 0 || current.asset.Header == "[]" {
			current.asset.Header = jsonOrEmpty(info.header)
			current.asset.Columns = jsonOrEmpty(info.header)
		}
		if current.asset.ColumnCount < info.columnCount {
			current.asset.ColumnCount = info.columnCount
		}
		if current.asset.StorageMode == StorageModeFull {
			current.rows += info.rowCount
			if info.rowStart > 0 && (current.rowStart == 0 || info.rowStart < current.rowStart) {
				current.rowStart = info.rowStart
			}
			if info.rowEnd > current.rowEnd {
				current.rowEnd = info.rowEnd
			}
		}
		for _, sample := range info.samples {
			if len(current.samples) < sampleRowsPerTable {
				current.samples = append(current.samples, sample)
			}
		}
		blockAsset[index] = current.asset.ID
	}
	assets := make([]models.TableAsset, 0, len(order))
	for _, sheet := range order {
		current := groups[sheet]
		if current.asset.StorageMode == "" {
			current.asset.StorageMode = StorageModeFull
		}
		if current.asset.StorageMode == StorageModeFull {
			if current.asset.ParsedRows == 0 {
				current.asset.ParsedRows = int64(current.rows)
			}
			if current.rowStart > 0 {
				current.asset.RowStart = current.rowStart
			}
			if current.rowEnd > 0 {
				current.asset.RowEnd = current.rowEnd
			}
		}
		current.asset.RowCount = int(current.asset.ParsedRows)
		if current.asset.TotalRows == 0 {
			current.asset.TotalRows = int64(current.asset.RowCount)
		}
		current.asset.SampleRows = jsonOrEmpty(sampleLines(current.samples))
		current.asset.Stats = fmt.Sprintf(
			`{"rows":%d,"parsedRows":%d,"columns":%d,"storageMode":%q,"parseStatus":%q}`,
			current.asset.TotalRows, current.asset.ParsedRows, current.asset.ColumnCount,
			current.asset.StorageMode, current.asset.ParseStatus,
		)
		assets = append(assets, current.asset)
	}
	return blockAsset, assets
}

func jsonOrEmpty(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func summarySourcePath(sheet string) string {
	if sheet == "" {
		sheet = "default"
	}
	return "sheet:" + sheet + "!summary"
}

func sampleLines(rows [][]string) []string {
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, strings.Join(row, " | "))
	}
	return lines
}

func tableRowModels(version models.DocumentVersion, assetID string, info tableMetaInfo) []models.TableRow {
	rows := make([]models.TableRow, 0, len(info.rows))
	for index, row := range info.rows {
		cells := map[string]string{}
		for column, value := range row {
			if column < len(info.header) {
				cells[info.header[column]] = value
			}
		}
		encoded, _ := json.Marshal(cells)
		rowNumber := info.rowStart + index
		rows = append(rows, models.TableRow{
			ID: stableID("trow", assetID, fmt.Sprint(rowNumber)), TenantID: version.TenantID,
			KnowledgeBaseID: version.KnowledgeBaseID, DocumentID: version.DocumentID,
			DocumentVersionID: version.ID, TableAssetID: assetID,
			RowNumber: rowNumber, Cells: string(encoded),
		})
	}
	return rows
}

func explicitIndexMode(mode string) bool {
	return mode == IndexModeStandard || mode == IndexModeDeep || mode == IndexModeLexical
}

func aggregateAssetMode(assets []models.TableAsset, fallback string) string {
	mode := ""
	for _, asset := range assets {
		if asset.IndexMode == "" {
			continue
		}
		if mode == "" {
			mode = asset.IndexMode
			continue
		}
		if mode != asset.IndexMode {
			return IndexModeStandard
		}
	}
	if mode == "" {
		return fallback
	}
	return mode
}

func mergeStorageMode(current, candidate string) string {
	rank := map[string]int{StorageModeFull: 0, StorageModeSampled: 1, StorageModeMetadata: 2}
	if rank[candidate] > rank[current] {
		return candidate
	}
	return current
}

func estimateTokens(chars int) int64 {
	if chars <= 0 {
		return 0
	}
	return int64((chars + charsPerTokenEstimate - 1) / charsPerTokenEstimate)
}
