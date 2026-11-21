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
)

const (
	smallTableMaxRows     = 2000
	smallTableMaxColumns  = 50
	mediumTableMaxRows    = 50000
	mediumTableMaxColumns = 100
	maxTableSheets        = 20
	sampleRowsPerTable    = 3
	charsPerTokenEstimate = 2
)

// tableDocProfile is the aggregated shape used to pick a default mode.
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
	samples     [][]string
	rows        [][]string
}

func tableMetaFor(block DocumentBlock) (tableMetaInfo, bool) {
	if block.Kind != "table" {
		return tableMetaInfo{}, false
	}
	lines := cleanTableLines(block.Text)
	if len(lines) == 0 {
		return tableMetaInfo{}, false
	}
	info := tableMetaInfo{}
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
		if row := splitTableLine(line); len(row) > 0 {
			info.rows = append(info.rows, row)
			if len(info.samples) < sampleRowsPerTable {
				info.samples = append(info.samples, row)
			}
		}
	}
	info.rowCount = len(info.rows)
	if info.rowEnd == 0 {
		info.rowEnd = info.rowStart + info.rowCount - 1
	}
	return info, true
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

func tableSummaryText(info tableMetaInfo) string {
	var builder strings.Builder
	if info.sheet != "" {
		builder.WriteString("表: " + info.sheet + "\n")
	}
	builder.WriteString("列: " + strings.Join(info.header, ", ") + "\n")
	fmt.Fprintf(&builder, "行数: %d，列数: %d\n", info.rowCount, info.columnCount)
	if len(info.samples) > 0 {
		builder.WriteString("样本行:\n")
		for _, row := range info.samples {
			builder.WriteString(strings.Join(row, " | ") + "\n")
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
	}
}

const maxPersistedTableRows = 200000

func buildTableAssets(version models.DocumentVersion, blocks []DocumentBlock) (map[int]string, []models.TableAsset) {
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
	for index, block := range blocks {
		info, ok := tableMetaFor(block)
		if !ok {
			continue
		}
		sheet := info.sheet
		if sheet == "" {
			sheet = "default"
		}
		current, exists := groups[sheet]
		if !exists {
			current = &group{asset: models.TableAsset{
				ID: stableID("tbl", version.ID, sheet), TenantID: version.TenantID, KnowledgeBaseID: version.KnowledgeBaseID,
				DocumentID: version.DocumentID, DocumentVersionID: version.ID, Sheet: sheet,
				Header: jsonOrEmpty(info.header), ColumnCount: info.columnCount, IndexMode: version.IndexMode,
			}, firstIndex: index}
			groups[sheet] = current
			order = append(order, sheet)
		}
		if current.asset.ColumnCount < info.columnCount {
			current.asset.ColumnCount = info.columnCount
		}
		current.rows += info.rowCount
		if info.rowStart > 0 && (current.rowStart == 0 || info.rowStart < current.rowStart) {
			current.rowStart = info.rowStart
		}
		if info.rowEnd > current.rowEnd {
			current.rowEnd = info.rowEnd
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
		current.asset.RowCount = current.rows
		current.asset.RowStart = current.rowStart
		current.asset.RowEnd = current.rowEnd
		current.asset.SampleRows = jsonOrEmpty(current.samples)
		current.asset.Stats = `{"rows":` + fmt.Sprint(current.rows) + `,"columns":` + fmt.Sprint(current.asset.ColumnCount) + `}`
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

func estimateTokens(chars int) int64 {
	if chars <= 0 {
		return 0
	}
	return int64((chars + charsPerTokenEstimate - 1) / charsPerTokenEstimate)
}
