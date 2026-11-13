// Package confighealth verifies the configuration chain the workbench exposes:
// model reachability, embedding dimension versus the vector index, RLS mode,
// backup freshness, license expiry, rerank reachability and drift between the
// running process and the stored/file configuration.
package confighealth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/configschema"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/modelregistry"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

const (
	StatusOK      = "ok"
	StatusWarning = "warning"
	StatusError   = "error"
	StatusUnknown = "unknown"
	StatusSkipped = "skipped"
)

const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityHigh    = "high"
)

// Check is one configuration verification.
type Check struct {
	ID          string
	Status      string
	Severity    string
	Summary     string
	Detail      string
	Remediation string
	CheckedAt   time.Time
}

// Report is the aggregate health view.
type Report struct {
	Status      string
	HighRisk    int
	Checks      []Check
	GeneratedAt time.Time
}

// ConnectionResult is one "test connection" outcome.
type ConnectionResult struct {
	Healthy   bool
	Error     string
	Dimension int
	CheckedAt time.Time
}

// Embedder is the narrow view of the ingestion embedding adapter the health
// check needs; the real adapter satisfies it.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dimension() int
	Name() string
}

// IndexStatus is one HNSW index as the health check needs it.
type IndexStatus struct {
	Name      string
	Dimension int
	Valid     bool
}

// IndexReport is the read-only index state. The composition root adapts the
// vector package into this shape so this package does not import an adapter.
type IndexReport struct {
	Indexes            []IndexStatus
	DimensionHistogram []DimensionCount
}

type DimensionCount struct {
	Dimension int
	Count     int64
}

// Service owns the checks. Probes are injectable so tests do not need network
// access or a live PostgreSQL.
type Service struct {
	db              *gorm.DB
	options         Options
	models          *modelregistry.Registry
	embedder        Embedder
	now             func() time.Time
	checkIndexes    func(context.Context) (IndexReport, error)
	indexDimension  func(context.Context) (int, bool, error)
	probeDeployment func(context.Context, model.Capability) (modelregistry.ProviderTestResult, error)
	probeProvider   func(context.Context, string, modelregistry.Actor, string) (modelregistry.ProviderTestResult, error)
}

type Options struct {
	DB *gorm.DB
	// Deployment values are passed as plain fields so this package does not
	// import the config loader.
	AppEnv              string
	EmbeddingDimension  int
	RerankSource        string
	BackupDir           string
	BackupMaxAgeHours   int
	LicenseExpiresAt    string
	RetrievalConfigFile string
	RetrievalFileHash   string
	Models              *modelregistry.Registry
	Embedder            Embedder
	Now                 func() time.Time
	// Index checks default to unavailable; the composition root supplies the
	// pgvector inspector.
	CheckIndexes   func(context.Context) (IndexReport, error)
	IndexDimension func(context.Context) (int, bool, error)
	// ProbeDeployment and ProbeProvider default to the model registry.
	ProbeDeployment func(context.Context, model.Capability) (modelregistry.ProviderTestResult, error)
	ProbeProvider   func(context.Context, string, modelregistry.Actor, string) (modelregistry.ProviderTestResult, error)
}

func NewService(options Options) *Service {
	service := &Service{
		db: options.DB, options: options, models: options.Models,
		embedder: options.Embedder, now: options.Now,
		checkIndexes:    options.CheckIndexes,
		indexDimension:  options.IndexDimension,
		probeDeployment: options.ProbeDeployment,
		probeProvider:   options.ProbeProvider,
	}
	if service.now == nil {
		service.now = time.Now
	}
	if service.checkIndexes == nil {
		service.checkIndexes = func(context.Context) (IndexReport, error) {
			return IndexReport{}, fmt.Errorf("index inspector is not configured")
		}
	}
	if service.indexDimension == nil {
		service.indexDimension = func(context.Context) (int, bool, error) {
			return 0, false, fmt.Errorf("index inspector is not configured")
		}
	}
	if service.probeDeployment == nil && options.Models != nil {
		service.probeDeployment = options.Models.TestDeploymentCapability
	}
	if service.probeProvider == nil && options.Models != nil {
		service.probeProvider = options.Models.TestProvider
	}
	return service
}

// Health runs every check for one tenant. DB-scoped checks only see the
// caller's tenant; the deployment tier is the same for everyone.
func (s *Service) Health(ctx context.Context, tenantID string) (Report, error) {
	now := s.now().UTC()
	checks := []Check{
		s.modelCheck(ctx, now, model.CapabilityChat),
		s.modelCheck(ctx, now, model.CapabilityEmbedding),
		s.modelCheck(ctx, now, model.CapabilityRerank),
		s.tenantModelCheck(ctx, tenantID, now),
		s.indexCheck(ctx, now),
		s.dimensionCheck(ctx, tenantID, now),
		s.rlsCheck(now),
		s.backupCheck(now),
		s.licenseCheck(now),
		s.fileDriftCheck(now),
		s.storedDriftCheck(ctx, tenantID, now),
	}
	report := Report{Checks: checks, GeneratedAt: now, Status: StatusOK}
	for _, check := range checks {
		if check.Severity == SeverityHigh && check.Status != StatusOK && check.Status != StatusSkipped && check.Status != StatusUnknown {
			report.HighRisk++
		}
		switch check.Status {
		case StatusError:
			report.Status = StatusError
		case StatusWarning:
			if report.Status != StatusError {
				report.Status = StatusWarning
			}
		}
	}
	return report, nil
}

// TestConnection probes one capability with the stored deployment or tenant
// provider. Credentials never come from the request body.
func (s *Service) TestConnection(ctx context.Context, tenantID string, actor modelregistry.Actor, capability, providerID string) (ConnectionResult, error) {
	now := s.now().UTC()
	if strings.TrimSpace(providerID) != "" {
		if s.probeProvider == nil {
			return ConnectionResult{}, apperror.Invalidf("model catalog is not available")
		}
		result, err := s.probeProvider(ctx, tenantID, actor, providerID)
		if err != nil {
			return ConnectionResult{}, err
		}
		return ConnectionResult{Healthy: result.Healthy, Error: result.Error, CheckedAt: now}, nil
	}
	kind, ok := parseCapability(capability)
	if !ok {
		return ConnectionResult{}, apperror.Invalidf("capability must be chat, embedding or rerank")
	}
	if s.probeDeployment == nil {
		return ConnectionResult{}, apperror.Invalidf("deployment models are not available")
	}
	result, err := s.probeDeployment(ctx, kind)
	if err != nil {
		return ConnectionResult{}, err
	}
	connection := ConnectionResult{Healthy: result.Healthy, Error: result.Error, CheckedAt: now}
	if kind == model.CapabilityEmbedding && result.Healthy {
		if s.embedder == nil {
			connection.Healthy = false
			connection.Error = "embedding adapter is not available"
			return connection, nil
		}
		vectors, err := s.embedder.Embed(ctx, []string{"configuration dimension probe"})
		if err != nil {
			connection.Healthy = false
			connection.Error = err.Error()
			return connection, nil
		}
		dimension := 0
		if len(vectors) > 0 {
			dimension = len(vectors[0])
		}
		connection.Dimension = dimension
		if dimension != s.options.EmbeddingDimension {
			connection.Healthy = false
			connection.Error = fmt.Sprintf("embedding returned dimension %d, configured %d", dimension, s.options.EmbeddingDimension)
		}
	}
	return connection, nil
}

func parseCapability(value string) (model.Capability, bool) {
	switch model.Capability(strings.ToLower(strings.TrimSpace(value))) {
	case model.CapabilityChat:
		return model.CapabilityChat, true
	case model.CapabilityEmbedding:
		return model.CapabilityEmbedding, true
	case model.CapabilityRerank:
		return model.CapabilityRerank, true
	default:
		return "", false
	}
}

func (s *Service) modelCheck(ctx context.Context, now time.Time, capability model.Capability) Check {
	id := "model." + string(capability)
	if s.models == nil {
		return newCheck(id, StatusUnknown, SeverityInfo, "模型目录不可用", "", "确认 API 进程已完成模型装配", now)
	}
	statuses := s.models.Health(ctx)
	relevant := make([]modelregistry.HealthStatus, 0, len(statuses))
	for _, status := range statuses {
		if status.Source == "deployment" && status.Capability == string(capability) {
			relevant = append(relevant, status)
		}
	}
	if len(relevant) == 0 {
		return newCheck(id, StatusWarning, SeverityHigh, "没有可用模型", "部署模型目录中没有承担 "+string(capability)+" 的模型", "在 env 或租户模型供应商中配置该能力", now)
	}
	failed := make([]string, 0)
	for _, status := range relevant {
		if status.Healthy {
			continue
		}
		message := status.Model + " (" + status.Provider + ")"
		if status.Error != "" {
			message += ": " + status.Error
		}
		failed = append(failed, message)
	}
	if len(failed) == 0 {
		return newCheck(id, StatusOK, SeverityInfo, "模型端点可达", strings.Join(modelNames(relevant), ", "), "", now)
	}
	severity := SeverityHigh
	if capability == model.CapabilityRerank && s.options.RerankSource == "none" {
		severity = SeverityWarning
	}
	return newCheck(id, StatusError, severity, "模型端点不可达", strings.Join(failed, "; "), "检查模型服务与网络，或在模型供应商页面测试连接", now)
}

func (s *Service) tenantModelCheck(ctx context.Context, tenantID string, now time.Time) Check {
	id := "model.tenant_providers"
	if s.models == nil {
		return newCheck(id, StatusSkipped, SeverityInfo, "租户模型供应商不可用", "", "", now)
	}
	if tenantID == "" {
		return newCheck(id, StatusSkipped, SeverityInfo, "无租户上下文", "", "", now)
	}
	statuses := s.models.Health(tenantdb.WithTenant(ctx, tenantID))
	tenantStatuses := make([]modelregistry.HealthStatus, 0)
	for _, status := range statuses {
		// Deployment entries keep the provider source; tenant entries name the
		// provider row, so a status whose provider belongs to no deployment entry
		// is tenant-owned. The registry reports both under one snapshot.
		if status.Source == "tenant" {
			tenantStatuses = append(tenantStatuses, status)
		}
	}
	if len(tenantStatuses) == 0 {
		return newCheck(id, StatusSkipped, SeverityInfo, "未配置租户模型供应商", "当前租户使用部署默认模型", "", now)
	}
	failed := make([]string, 0)
	for _, status := range tenantStatuses {
		if !status.Healthy {
			failed = append(failed, status.Model+": "+status.Error)
		}
	}
	if len(failed) == 0 {
		return newCheck(id, StatusOK, SeverityInfo, "租户模型供应商可达", strings.Join(modelNames(tenantStatuses), ", "), "", now)
	}
	return newCheck(id, StatusError, SeverityHigh, "租户模型供应商不可达", strings.Join(failed, "; "), "在租户模型供应商页面重新测试连接或更新凭据", now)
}

func modelNames(statuses []modelregistry.HealthStatus) []string {
	names := make([]string, 0, len(statuses))
	for _, status := range statuses {
		names = append(names, status.Model)
	}
	return names
}

func (s *Service) indexCheck(ctx context.Context, now time.Time) Check {
	const id = "index.valid"
	if s.db == nil {
		return newCheck(id, StatusUnknown, SeverityInfo, "无法检查向量索引", "数据库不可用", "", now)
	}
	report, err := s.checkIndexes(ctx)
	if err != nil {
		return newCheck(id, StatusUnknown, SeverityHigh, "无法检查向量索引", err.Error(), "确认数据库连接与 pgvector 扩展", now)
	}
	if len(report.Indexes) == 0 {
		return newCheck(id, StatusWarning, SeverityHigh, "尚未建立 HNSW 索引", "检索将退化为顺序扫描", "运行 make embed-index 建立索引", now)
	}
	invalid := make([]string, 0)
	for _, status := range report.Indexes {
		if !status.Valid {
			invalid = append(invalid, status.Name)
		}
	}
	if len(invalid) > 0 {
		return newCheck(id, StatusError, SeverityHigh, "存在无效向量索引", strings.Join(invalid, ", "), "重建失败的索引（make embed-index）", now)
	}
	return newCheck(id, StatusOK, SeverityInfo, "向量索引有效", fmt.Sprintf("%d 个索引", len(report.Indexes)), "", now)
}

func (s *Service) dimensionCheck(ctx context.Context, tenantID string, now time.Time) Check {
	const id = "embedding.dimension"
	if s.db == nil {
		return newCheck(id, StatusUnknown, SeverityInfo, "无法检查 embedding 维度", "数据库不可用", "", now)
	}
	indexDimension, found, err := s.indexDimension(ctx)
	if err != nil {
		return newCheck(id, StatusUnknown, SeverityHigh, "无法读取索引维度", err.Error(), "确认 pgvector 索引目录可读", now)
	}
	if !found {
		return newCheck(id, StatusWarning, SeverityHigh, "没有可用的 serving 索引", "无法确认向量维度", "运行 make embed-index", now)
	}
	if indexDimension != s.options.EmbeddingDimension {
		return newCheck(id, StatusError, SeverityHigh, "部署 embedding 维度与索引不一致",
			fmt.Sprintf("EMBEDDING_DIMENSION=%d，索引维度=%d", s.options.EmbeddingDimension, indexDimension),
			"要么恢复原维度，要么运行 make embed-rebuild 迁移索引", now)
	}
	if tenantID != "" {
		var bindings []struct {
			ID                 string
			EmbeddingDimension int
		}
		if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.KnowledgeBase{}).
			Select("id, embedding_dimension").Find(&bindings).Error; err != nil {
			return newCheck(id, StatusUnknown, SeverityInfo, "无法读取知识库绑定", err.Error(), "", now)
		}
		mismatched := make([]string, 0)
		for _, binding := range bindings {
			if binding.EmbeddingDimension != 0 && binding.EmbeddingDimension != indexDimension {
				mismatched = append(mismatched, fmt.Sprintf("%s=%d", binding.ID, binding.EmbeddingDimension))
			}
		}
		if len(mismatched) > 0 {
			return newCheck(id, StatusError, SeverityHigh, "知识库 embedding 绑定与索引不一致", strings.Join(mismatched, ", "), "对该知识库执行托管重建（D3）", now)
		}
	}
	return newCheck(id, StatusOK, SeverityInfo, "embedding 维度一致", fmt.Sprintf("维度 %d", indexDimension), "", now)
}

func (s *Service) rlsCheck(now time.Time) Check {
	const id = "rls.mode"
	mode := strings.ToLower(strings.TrimSpace(string(tenantdb.CurrentRLSMode())))
	if mode == "" {
		mode = "off"
	}
	if s.options.AppEnv == "production" && mode != "enforce" {
		return newCheck(id, StatusError, SeverityHigh, "生产环境未强制 RLS", "TENANT_RLS_MODE="+mode, "生产必须使用 enforce 并拆分数据库角色", now)
	}
	if mode == "shadow" {
		return newCheck(id, StatusWarning, SeverityHigh, "RLS 处于 shadow 模式", "shadow 只记录越权写，不阻断", "生产切换为 enforce 并复跑 RLS 负向用例", now)
	}
	return newCheck(id, StatusOK, SeverityInfo, "RLS 模式可用", mode, "", now)
}

func (s *Service) backupCheck(now time.Time) Check {
	const id = "backup.age"
	if strings.TrimSpace(s.options.BackupDir) == "" {
		return newCheck(id, StatusUnknown, SeverityInfo, "未配置备份目录", "BACKUP_DIR 为空，无法校验备份时效", "挂载备份目录并设置 BACKUP_DIR 与 BACKUP_MAX_AGE_HOURS", now)
	}
	latest, found, err := newestBackup(s.options.BackupDir)
	if err != nil {
		return newCheck(id, StatusWarning, SeverityHigh, "备份目录不可读", err.Error(), "确认 BACKUP_DIR 挂载与权限", now)
	}
	if !found {
		return newCheck(id, StatusWarning, SeverityHigh, "尚无备份文件", s.options.BackupDir, "运行 make backup-postgres 或 backup-basebackup", now)
	}
	maxAge := time.Duration(s.options.BackupMaxAgeHours) * time.Hour
	if maxAge <= 0 {
		maxAge = 24 * time.Hour
	}
	age := now.Sub(latest)
	if age > maxAge {
		return newCheck(id, StatusWarning, SeverityHigh, "备份已过期",
			fmt.Sprintf("最近备份 %s，已 %s", latest.Format(time.RFC3339), age.Round(time.Minute)),
			"检查定时备份任务并立即执行一次备份", now)
	}
	return newCheck(id, StatusOK, SeverityInfo, "备份时效正常", fmt.Sprintf("最近备份 %s", latest.Format(time.RFC3339)), "", now)
}

func newestBackup(dir string) (time.Time, bool, error) {
	var latest time.Time
	found := false
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name := strings.ToLower(entry.Name())
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".json") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !found || info.ModTime().After(latest) {
			latest = info.ModTime()
			found = true
		}
		return nil
	})
	return latest, found, err
}

func (s *Service) licenseCheck(now time.Time) Check {
	const id = "license.expiry"
	value := strings.TrimSpace(s.options.LicenseExpiresAt)
	if value == "" {
		return newCheck(id, StatusUnknown, SeverityInfo, "未配置许可证", "LICENSE_EXPIRES_AT 为空；当前没有许可签发机制", "商业分发前接入许可证并把到期日写入 LICENSE_EXPIRES_AT", now)
	}
	expires, err := parseLicenseDate(value)
	if err != nil {
		return newCheck(id, StatusWarning, SeverityHigh, "许可证日期无效", err.Error(), "使用 RFC3339 或 YYYY-MM-DD", now)
	}
	if now.After(expires) {
		return newCheck(id, StatusError, SeverityHigh, "许可证已过期", expires.Format(time.RFC3339), "更新许可证或停止对外服务", now)
	}
	if expires.Sub(now) < 30*24*time.Hour {
		return newCheck(id, StatusWarning, SeverityHigh, "许可证即将到期", expires.Format(time.RFC3339), "提前续期", now)
	}
	return newCheck(id, StatusOK, SeverityInfo, "许可证有效", expires.Format(time.RFC3339), "", now)
}

func parseLicenseDate(value string) (time.Time, error) {
	layouts := []string{time.RFC3339, time.RFC3339Nano, "2006-01-02"}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("LICENSE_EXPIRES_AT must be RFC3339 or YYYY-MM-DD, got %q", value)
}

func (s *Service) fileDriftCheck(now time.Time) Check {
	const id = "drift.retrieval_file"
	path := strings.TrimSpace(s.options.RetrievalConfigFile)
	if path == "" {
		return newCheck(id, StatusSkipped, SeverityInfo, "未使用检索配置文件", "", "", now)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && s.options.RetrievalFileHash == "" {
			return newCheck(id, StatusOK, SeverityInfo, "检索配置文件未启用", "启动时文件不存在，使用 schema 默认值", "", now)
		}
		return newCheck(id, StatusWarning, SeverityInfo, "无法读取检索配置文件", err.Error(), "确认 RETRIEVAL_CONFIG_FILE 可读", now)
	}
	sum := sha256.Sum256(raw)
	current := hex.EncodeToString(sum[:])
	if s.options.RetrievalFileHash == "" {
		return newCheck(id, StatusWarning, SeverityHigh, "检索配置文件在启动后出现", path, "重启 API/Worker 使配置生效", now)
	}
	if current != s.options.RetrievalFileHash {
		return newCheck(id, StatusWarning, SeverityHigh, "检索配置文件已变更但进程未重启", path, "重启 API/Worker 使部署层配置生效", now)
	}
	return newCheck(id, StatusOK, SeverityInfo, "检索配置文件与进程一致", path, "", now)
}

func (s *Service) storedDriftCheck(ctx context.Context, tenantID string, now time.Time) Check {
	const id = "drift.stored_config"
	if s.db == nil || tenantID == "" {
		return newCheck(id, StatusSkipped, SeverityInfo, "无法检查存储配置", "", "", now)
	}
	problems := make([]string, 0)
	var tenant models.Tenant
	if err := tenantdb.FromContext(ctx, s.db).Where("id = ?", tenantID).Take(&tenant).Error; err == nil {
		problems = append(problems, validateStored("tenant.retrieval", configschema.DomainRetrieval, tenant.RetrievalConfig)...)
		problems = append(problems, validateStored("tenant.chunking", configschema.DomainChunking, tenant.ChunkingConfig)...)
	}
	var knowledgeBases []models.KnowledgeBase
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.KnowledgeBase{}).
		Select("id, retrieval_config").Find(&knowledgeBases).Error; err == nil {
		for _, knowledgeBase := range knowledgeBases {
			problems = append(problems, validateStored("knowledge_base."+knowledgeBase.ID, configschema.DomainRetrieval, knowledgeBase.RetrievalConfig)...)
		}
	}
	if len(problems) > 0 {
		return newCheck(id, StatusError, SeverityHigh, "存储配置与 schema 漂移", strings.Join(problems, "; "),
			"通过设置页重新保存，或按 schema 修正数据库中的未知键/越界值", now)
	}
	return newCheck(id, StatusOK, SeverityInfo, "存储配置与 schema 一致", "", "", now)
}

func validateStored(label, domain, raw string) []string {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "{}" {
		return nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return []string{label + ": " + err.Error()}
	}
	if err := configschema.Validate(domain, values); err != nil {
		return []string{label + ": " + err.Error()}
	}
	return nil
}

func newCheck(id, status, severity, summary, detail, remediation string, now time.Time) Check {
	return Check{ID: id, Status: status, Severity: severity, Summary: summary, Detail: detail, Remediation: remediation, CheckedAt: now}
}
