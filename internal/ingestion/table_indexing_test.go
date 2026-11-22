package ingestion

import (
	"testing"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func tableBlock(sheet string, rows ...string) DocumentBlock {
	header := []string{"name", "value"}
	return DocumentBlock{
		Kind: "table", SourcePath: "sheet:" + sheet + "!A1:B9", Text: "name | value\n" + joinRows(rows),
		Table: &TableMeta{Sheet: sheet, Header: header, RowStart: 2, RowEnd: 1 + len(rows), ColumnCount: 2},
	}
}

func joinRows(rows []string) string {
	result := ""
	for index, row := range rows {
		if index > 0 {
			result += "\n"
		}
		result += row
	}
	return result
}

func TestResolveIndexModeTiers(t *testing.T) {
	small := tableBlock("small", "a | 1", "b | 2")
	if mode := resolveIndexMode("", []DocumentBlock{small}); mode != IndexModeDeep {
		t.Fatalf("small table mode = %s, want deep", mode)
	}
	if mode := resolveIndexMode(IndexModeLexical, []DocumentBlock{small}); mode != IndexModeLexical {
		t.Fatalf("explicit mode = %s, want lexical", mode)
	}
	if mode := resolveIndexMode("", []DocumentBlock{{Kind: "paragraph", Text: "plain"}}); mode != IndexModeStandard {
		t.Fatalf("text-only mode = %s, want standard", mode)
	}
	smallProfile := tableDocProfile{rows: 500, maxCols: 10, sheets: map[string]bool{"s": true}}
	if mode := modeForProfile(smallProfile); mode != IndexModeDeep {
		t.Fatalf("small profile = %s", mode)
	}
	mediumProfile := tableDocProfile{rows: 20000, maxCols: 20, sheets: map[string]bool{"s": true}}
	if mode := modeForProfile(mediumProfile); mode != IndexModeStandard {
		t.Fatalf("medium profile = %s", mode)
	}
	largeProfile := tableDocProfile{rows: 200000, maxCols: 20, sheets: map[string]bool{"s": true}}
	if mode := modeForProfile(largeProfile); mode != IndexModeLexical {
		t.Fatalf("large profile = %s", mode)
	}
	wideProfile := tableDocProfile{rows: 100, maxCols: 200, sheets: map[string]bool{"s": true}}
	if mode := modeForProfile(wideProfile); mode != IndexModeLexical {
		t.Fatalf("wide profile = %s", mode)
	}
}

func TestTableMetaFallbackAndSummary(t *testing.T) {
	block := DocumentBlock{Kind: "table", SourcePath: "sheet:Sheet1!A1:C3", Text: "name | qty\npen | 2\ntape | 5"}
	info, ok := tableMetaFor(block)
	if !ok {
		t.Fatalf("table meta not derived")
	}
	if info.sheet != "Sheet1" || info.rowCount != 2 || info.columnCount != 2 {
		t.Fatalf("info = %+v", info)
	}
	sum := tableSummaryText(info)
	for _, want := range []string{"Sheet1", "name", "qty", "pen"} {
		if !contains(sum, want) {
			t.Fatalf("summary %q missing %q", sum, want)
		}
	}
}

func contains(value, part string) bool {
	for index := 0; index+len(part) <= len(value); index++ {
		if value[index:index+len(part)] == part {
			return true
		}
	}
	return false
}

func TestBuildTableAssetsKeepsSampledAndMetadataSheets(t *testing.T) {
	version := models.DocumentVersion{ID: "ver-b38", TenantID: "t", KnowledgeBaseID: "kb", DocumentID: "doc"}
	sheets := []SheetMeta{
		{Name: "small", Header: []string{"a", "b"}, DeclaredRows: 2, TotalRows: 2, ParsedRows: 2, ColumnCount: 2, ParseStatus: ParseStatusParsed},
		{Name: "huge", Header: []string{"x", "y"}, DeclaredRows: 50000, TotalRows: 50000, ParsedRows: 50, ColumnCount: 2, ParseStatus: ParseStatusSampled, Truncated: true, SampleRows: []string{"x | y"}},
		{Name: "catalog", Header: []string{"m", "n"}, DeclaredRows: 6000000, TotalRows: 6000000, ColumnCount: 2, ParseStatus: ParseStatusMetadata, Truncated: true},
	}
	blocks := []DocumentBlock{
		{Kind: "table", SourcePath: "sheet:small!A2:B3", Text: "a | b\nx | 1\ny | 2",
			Table: &TableMeta{Sheet: "small", Header: []string{"a", "b"}, RowStart: 2, RowEnd: 3, ColumnCount: 2}},
	}
	_, assets := buildTableAssets(version, blocks, sheets)
	if len(assets) != 3 {
		t.Fatalf("assets = %d, want 3", len(assets))
	}
	bySheet := map[string]models.TableAsset{}
	for _, asset := range assets {
		bySheet[asset.Sheet] = asset
	}
	if bySheet["small"].StorageMode != StorageModeFull || bySheet["small"].ParsedRows != 2 {
		t.Fatalf("small asset = %+v", bySheet["small"])
	}
	if bySheet["huge"].StorageMode != StorageModeSampled || bySheet["huge"].ParsedRows != 50 {
		t.Fatalf("huge asset = %+v", bySheet["huge"])
	}
	if bySheet["catalog"].StorageMode != StorageModeMetadata || bySheet["catalog"].TotalRows != 6000000 {
		t.Fatalf("catalog asset = %+v", bySheet["catalog"])
	}
	for index := range assets {
		if assets[index].ID == "" || assets[index].Sheet == "" {
			t.Fatalf("asset has no identity: %+v", assets[index])
		}
	}
}

func TestResolveSheetIndexModeUsesExplicitOverride(t *testing.T) {
	profile := tableDocProfile{rows: 100000, sheets: map[string]bool{"s": true}, maxCols: 10}
	asset := models.TableAsset{TotalRows: 100, ColumnCount: 2, StorageMode: StorageModeFull}
	if mode := resolveSheetIndexMode(IndexModeLexical, asset, profile); mode != IndexModeLexical {
		t.Fatalf("explicit mode = %s, want lexical", mode)
	}
	asset.StorageMode = StorageModeSampled
	if mode := resolveSheetIndexMode("", asset, profile); mode != IndexModeStandard {
		t.Fatalf("sampled mode = %s, want standard", mode)
	}
	if mode := aggregateAssetMode([]models.TableAsset{{IndexMode: IndexModeLexical}, {IndexMode: IndexModeStandard}}, IndexModeDeep); mode != IndexModeStandard {
		t.Fatalf("aggregate mode = %s, want standard", mode)
	}
}

func TestBuildTableAssetsGroupsBySheet(t *testing.T) {
	version := models.DocumentVersion{
		ID: "ver-x", TenantID: "t", KnowledgeBaseID: "kb", DocumentID: "doc",
	}
	blocks := []DocumentBlock{
		{Kind: "table", SourcePath: "sheet:S1!A1:B3", Text: "a | b\nx | 1\ny | 2",
			Table: &TableMeta{Sheet: "S1", Header: []string{"a", "b"}, RowStart: 2, RowEnd: 3, ColumnCount: 2}},
		{Kind: "table", SourcePath: "sheet:S1!A4:B5", Text: "a | b\nz | 3",
			Table: &TableMeta{Sheet: "S1", Header: []string{"a", "b"}, RowStart: 4, RowEnd: 4, ColumnCount: 2}},
		{Kind: "table", SourcePath: "sheet:S2!A1:B2", Text: "c | d\nq | 9",
			Table: &TableMeta{Sheet: "S2", Header: []string{"c", "d"}, RowStart: 2, RowEnd: 2, ColumnCount: 2}},
	}
	blockAsset, assets := buildTableAssets(version, blocks, nil)
	if len(assets) != 2 {
		t.Fatalf("assets = %d, want 2", len(assets))
	}
	if blockAsset[0] != blockAsset[1] || blockAsset[0] == blockAsset[2] {
		t.Fatalf("block asset mapping = %v", blockAsset)
	}
	if assets[0].RowCount != 3 || assets[0].RowStart != 2 || assets[0].RowEnd != 4 {
		t.Fatalf("sheet S1 asset = %+v", assets[0])
	}
	rows := tableRowModels(version, assets[0].ID, tableMetaInfo{
		header: []string{"a", "b"}, rowStart: 2, rows: [][]string{{"x", "1"}, {"y", "2"}},
	})
	if len(rows) != 2 || rows[0].ID == rows[1].ID || rows[0].TableAssetID != assets[0].ID {
		t.Fatalf("rows = %+v", rows)
	}
}
