import { ApiOutlined, CloudDownloadOutlined, PlusOutlined } from "@ant-design/icons";
import {
  Alert,
  App,
  Button,
  Card,
  Col,
  Divider,
  Flex,
  Input,
  Modal,
  Row,
  Select,
  Space,
  Table,
  Tabs,
  Tag,
  Typography,
  type TableColumnsType,
} from "antd";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { api, apiBase, jsonInit } from "../api/client";
import { queryKeys, useKnowledgeBases } from "../api/hooks";
import type { APIKeyCreateRequest, APIKeyCreateResponse } from "../api/types";
import { PageHeader } from "../components/PageHeader";
import { QueryFallback } from "../components/QueryFallback";
import { usePermissions } from "../session";
import { formatTime } from "../utils/format";

export const mcpCapabilities = ["knowledge_base:read", "chat"] as const;

export const mcpExpiryOptions = [7, 30, 90, 0] as const;

export const mcpReleaseUrl = "https://github.com/hzc19970630/rag-platform/releases/latest";

export const mcpClients = ["qoder", "claude", "cursor", "generic"] as const;

export type McpClient = (typeof mcpClients)[number];

export const mcpTools = [
  { name: "list_knowledge_bases", access: "read" },
  { name: "list_documents", access: "read" },
  { name: "get_document", access: "read" },
  { name: "get_document_content", access: "read" },
  { name: "get_document_status", access: "read" },
  { name: "search", access: "read" },
  { name: "get_citation", access: "read" },
  { name: "ask", access: "generate" },
] as const;

export type McpTool = (typeof mcpTools)[number];

type SelfCheckKey = "key" | "documents" | "search";
type SelfCheckStatus = "running" | "ok" | "error" | "skipped";

type SelfCheckResult = {
  key: SelfCheckKey;
  status: SelfCheckStatus;
  detail: string;
  ms?: number;
};

type IssuedKey = {
  token: string;
  name: string;
  knowledgeBaseIds: string[];
  knowledgeBaseNames: string[];
  expiresAt?: string;
  days: number;
};

export function mcpExpiry(days: number, now = new Date()): string | undefined {
  if (days <= 0) return undefined;
  return new Date(now.getTime() + days * 24 * 60 * 60 * 1000).toISOString();
}

export function mcpDefaultKeyName(knowledgeBases: { name: string }[]): string {
  if (knowledgeBases.length === 0) return "";
  if (knowledgeBases.length === 1) return `mcp-${knowledgeBases[0].name}`;
  return `mcp-${knowledgeBases.length}-knowledge-bases`;
}

export function mcpKeyRequest(name: string, knowledgeBaseIds: string[], expiresAt?: string): APIKeyCreateRequest {
  return {
    name,
    capabilities: [...mcpCapabilities],
    knowledgeBaseIds,
    ...(expiresAt ? { expiresAt } : {}),
  };
}

export function buildMcpClientConfig(input: { apiUrl: string; apiKey: string; command: string }): string {
  return JSON.stringify(
    {
      mcpServers: {
        "rag-platform": {
          command: input.command,
          env: {
            RAG_API_URL: input.apiUrl,
            RAG_API_KEY: input.apiKey,
          },
        },
      },
    },
    null,
    2,
  );
}

export function evidenceCount(body: unknown): number {
  const payload = body as { evidence?: { items?: unknown[] }; items?: unknown[] } | undefined;
  const items = payload?.evidence?.items ?? payload?.items;
  return Array.isArray(items) ? items.length : 0;
}

export function McpPage() {
  const { t } = useTranslation();
  const { message, modal } = App.useApp();
  const queryClient = useQueryClient();
  const { canAdmin } = usePermissions();
  const knowledgeBases = useKnowledgeBases();

  const [knowledgeBaseIds, setKnowledgeBaseIds] = useState<string[]>([]);
  const [keyName, setKeyName] = useState("");
  const [expiryDays, setExpiryDays] = useState(30);
  const [creating, setCreating] = useState(false);
  const [issued, setIssued] = useState<IssuedKey>();
  const [tokenOpen, setTokenOpen] = useState(false);
  const [apiUrl, setApiUrl] = useState(apiBase);
  const [command, setCommand] = useState("rag-mcp");
  const [pastedKey, setPastedKey] = useState("");
  const [checking, setChecking] = useState(false);
  const [checks, setChecks] = useState<SelfCheckResult[]>([]);
  const firstKnowledgeBaseId = knowledgeBases.data?.[0]?.id;

  useEffect(() => {
    if (knowledgeBaseIds.length === 0 && firstKnowledgeBaseId) setKnowledgeBaseIds([firstKnowledgeBaseId]);
  }, [knowledgeBaseIds.length, firstKnowledgeBaseId]);

  if (!canAdmin) {
    return (
      <div className="page">
        <Alert type="error" showIcon message={t("common.noPermission")} description={t("mcp.noPermission")} />
      </div>
    );
  }

  const selectedKnowledgeBases = (knowledgeBases.data ?? []).filter((kb) => knowledgeBaseIds.includes(kb.id));
  const effectiveName = keyName.trim() || mcpDefaultKeyName(selectedKnowledgeBases);
  const scopeNames = selectedKnowledgeBases.map((kb) => kb.name);
  const scopeLabel = scopeNames.join(", ");
  const clientConfig = buildMcpClientConfig({
    apiUrl: apiUrl.trim() || apiBase,
    apiKey: issued?.token ?? "${RAG_API_KEY}",
    command: command.trim() || "rag-mcp",
  });

  async function createKey() {
    const expiresAt = mcpExpiry(expiryDays);
    setCreating(true);
    try {
      const result = await api<APIKeyCreateResponse>(
        "/api-keys",
        jsonInit(mcpKeyRequest(effectiveName, knowledgeBaseIds, expiresAt)),
      );
      setIssued({
        token: result.token,
        name: effectiveName,
        knowledgeBaseIds: [...knowledgeBaseIds],
        knowledgeBaseNames: scopeNames,
        expiresAt,
        days: expiryDays,
      });
      setChecks([]);
      setPastedKey("");
      setTokenOpen(true);
      await queryClient.invalidateQueries({ queryKey: queryKeys.apiKeys });
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("mcp.createKeyFailed"));
    } finally {
      setCreating(false);
    }
  }

  function requestCreate() {
    if (knowledgeBaseIds.length === 0) {
      message.warning(t("mcp.selectKnowledgeBaseFirst"));
      return;
    }
    if (knowledgeBaseIds.length === 1) {
      void createKey();
      return;
    }
    modal.confirm({
      title: t("mcp.multiConfirmTitle"),
      content: t("mcp.multiConfirmBody", { count: knowledgeBaseIds.length, scope: scopeLabel }),
      okText: t("mcp.createKey"),
      cancelText: t("common.cancel"),
      onOk: () => createKey(),
    });
  }

  function updateCheck(key: SelfCheckKey, patch: Partial<SelfCheckResult>) {
    setChecks((previous) => previous.map((item) => (item.key === key ? { ...item, ...patch } : item)));
  }

  function skipAfter(index: number) {
    setChecks((previous) => previous.map((item, position) => (
      position > index ? { ...item, status: "skipped", detail: t("mcp.checkSkipped") } : item
    )));
  }

  async function probe(path: string, token: string, init?: RequestInit) {
    const started = performance.now();
    try {
      const response = await fetch(`${apiBase}${path}`, {
        ...init,
        credentials: "omit",
        headers: {
          Authorization: `Bearer ${token}`,
          "Content-Type": "application/json",
          ...(init?.headers ?? {}),
        },
      });
      const ms = Math.round(performance.now() - started);
      let body: unknown;
      try {
        body = await response.clone().json();
      } catch {
        body = undefined;
      }
      if (response.ok) return { ok: true, status: response.status, ms, detail: `HTTP ${response.status}`, body };
      const error = body as { error?: string; message?: string } | undefined;
      const detail = [error?.error, error?.message].filter(Boolean).join(": ") || `HTTP ${response.status}`;
      return { ok: false, status: response.status, ms, detail: `${detail} ${checkHint(response.status)}`.trim(), body };
    } catch {
      return { ok: false, status: 0, ms: Math.round(performance.now() - started), detail: t("mcp.checkNetworkError"), body: undefined };
    }
  }

  function checkHint(status: number): string {
    if (status === 401) return t("mcp.check401");
    if (status === 403) return t("mcp.check403");
    if (status === 409) return t("mcp.check409");
    return "";
  }

  async function runSelfCheck(token: string, kbIds: string[]) {
    if (kbIds.length === 0) {
      message.warning(t("mcp.selectKnowledgeBaseFirst"));
      return;
    }
    const trimmed = token.trim();
    if (!trimmed) {
      message.warning(t("mcp.pasteKeyFirst"));
      return;
    }
    setChecking(true);
    setChecks([
      { key: "key", status: "running", detail: "" },
      { key: "documents", status: "running", detail: "" },
      { key: "search", status: "running", detail: "" },
    ]);
    try {
      const list = await probe("/knowledge-bases", trimmed);
      updateCheck("key", { status: list.ok ? "ok" : "error", detail: list.detail, ms: list.ms });
      if (!list.ok) {
        skipAfter(0);
        return;
      }

      let documentsOk = true;
      let documentsDetail = t("mcp.checkDocumentsOk", { count: kbIds.length });
      for (const id of kbIds) {
        const result = await probe(`/knowledge-bases/${id}/documents?limit=1`, trimmed);
        if (!result.ok) {
          const name = knowledgeBases.data?.find((kb) => kb.id === id)?.name ?? id;
          documentsDetail = `${name}: ${result.detail}`;
          documentsOk = false;
          break;
        }
      }
      updateCheck("documents", { status: documentsOk ? "ok" : "error", detail: documentsDetail });
      if (!documentsOk) {
        skipAfter(1);
        return;
      }

      const search = await probe("/search", trimmed, {
        method: "POST",
        body: JSON.stringify({ query: "hybrid recall", knowledgeBaseId: kbIds[0] }),
      });
      updateCheck("search", {
        status: search.ok ? "ok" : "error",
        ms: search.ms,
        detail: search.ok ? t("mcp.checkSearchOk", { count: evidenceCount(search.body) }) : search.detail,
      });
    } finally {
      setChecking(false);
    }
  }

  const knowledgeBaseOptions = (knowledgeBases.data ?? []).map((kb) => ({ value: kb.id, label: kb.name }));

  const toolColumns: TableColumnsType<McpTool> = [
    {
      title: t("mcp.toolNameColumn"),
      key: "name",
      width: 220,
      render: (_, row) => <Typography.Text className="mono-small">{row.name}</Typography.Text>,
    },
    {
      title: t("mcp.toolAccessColumn"),
      key: "access",
      width: 120,
      render: (_, row) => (
        <Tag color={row.access === "read" ? "green" : "gold"}>{t(`mcp.access.${row.access}`)}</Tag>
      ),
    },
    {
      title: t("mcp.toolDescriptionColumn"),
      key: "description",
      render: (_, row) => t(`mcp.tools.${row.name}`),
    },
  ];

  const checkTag = (status: SelfCheckStatus) => {
    if (status === "running") return <Tag color="processing">{t("mcp.checkRunning")}</Tag>;
    if (status === "ok") return <Tag color="green">{t("mcp.checkOk")}</Tag>;
    if (status === "error") return <Tag color="red">{t("mcp.checkFailed")}</Tag>;
    return <Tag>{t("mcp.checkSkipped")}</Tag>;
  };

  return (
    <div className="page">
      <PageHeader title={t("mcp.title")} description={t("mcp.description")} />

      <div className="ops-stack">
        <Alert type="info" showIcon message={t("mcp.boundary")} />

        <Row gutter={[16, 16]}>
          <Col xs={24} xl={10}>
            <Card title={t("mcp.keyStepTitle")}>
              <Flex vertical gap={16}>
                <div>
                  <Typography.Text strong>{t("mcp.knowledgeBases")}</Typography.Text>
                  <Select
                    className="mcp-field"
                    mode="multiple"
                    showSearch
                    optionFilterProp="label"
                    loading={knowledgeBases.isLoading}
                    value={knowledgeBaseIds}
                    placeholder={t("mcp.knowledgeBasePlaceholder")}
                    options={knowledgeBaseOptions}
                    onChange={(values) => setKnowledgeBaseIds(values)}
                  />
                </div>
                <div>
                  <Typography.Text strong>{t("mcp.keyName")}</Typography.Text>
                  <Input
                    className="mcp-field"
                    value={keyName}
                    placeholder={mcpDefaultKeyName(selectedKnowledgeBases) || t("mcp.keyNamePlaceholder")}
                    onChange={(event) => setKeyName(event.target.value)}
                  />
                </div>
                <div>
                  <Typography.Text strong>{t("mcp.expiry")}</Typography.Text>
                  <Select
                    className="mcp-field"
                    value={expiryDays}
                    onChange={setExpiryDays}
                    options={mcpExpiryOptions.map((days) => ({
                      value: days,
                      label: days === 0 ? t("mcp.expiryNever") : t("mcp.expiryDays", { days }),
                    }))}
                  />
                </div>
                {knowledgeBases.error && <QueryFallback error={knowledgeBases.error} retry={() => void knowledgeBases.refetch()} />}
                {knowledgeBases.data && knowledgeBases.data.length === 0 && (
                  <Alert type="warning" showIcon message={t("mcp.noKnowledgeBases")} />
                )}
                <Button
                  type="primary"
                  icon={<PlusOutlined />}
                  loading={creating}
                  disabled={knowledgeBaseIds.length === 0}
                  onClick={requestCreate}
                >
                  {t("mcp.createKey")}
                </Button>
                <Alert type="info" showIcon message={t("mcp.scopeNote")} />
              </Flex>
            </Card>
          </Col>

          <Col xs={24} xl={14}>
            <Card title={t("mcp.configStepTitle")}>
              <Flex vertical gap={16}>
                <Flex gap={12} wrap>
                  <div className="mcp-inline-field">
                    <Typography.Text strong>{t("mcp.apiUrl")}</Typography.Text>
                    <Input value={apiUrl} onChange={(event) => setApiUrl(event.target.value)} />
                  </div>
                  <div className="mcp-inline-field">
                    <Typography.Text strong>{t("mcp.binaryPath")}</Typography.Text>
                    <Input value={command} onChange={(event) => setCommand(event.target.value)} />
                  </div>
                </Flex>
                {issued ? (
                  <Alert
                    type="success"
                    showIcon
                    message={t("mcp.keyIssued", { name: issued.name, scope: issued.knowledgeBaseNames.join(", ") })}
                    description={issued.expiresAt ? t("mcp.expiresAt", { days: issued.days, date: formatTime(issued.expiresAt) }) : t("mcp.neverExpires")}
                  />
                ) : (
                  <Alert type="warning" showIcon message={t("mcp.keyRequiredForConfig")} />
                )}
                <Typography.Paragraph className="token-box" copyable={{ text: clientConfig }}>
                  <pre className="ops-report">{clientConfig}</pre>
                </Typography.Paragraph>
                <Tabs
                  size="small"
                  items={mcpClients.map((client) => ({
                    key: client,
                    label: t(`mcp.clients.${client}.label`),
                    children: <Typography.Text type="secondary">{t(`mcp.clients.${client}.location`)}</Typography.Text>,
                  }))}
                />
                <Divider style={{ margin: 0 }} />
                <div>
                  <Typography.Title level={5}>{t("mcp.selfCheckTitle")}</Typography.Title>
                  <Typography.Paragraph type="secondary">{t("mcp.selfCheckHint")}</Typography.Paragraph>
                  {issued ? (
                    <Button loading={checking} onClick={() => void runSelfCheck(issued.token, issued.knowledgeBaseIds)}>
                      {t("mcp.runSelfCheck")}
                    </Button>
                  ) : (
                    <Space.Compact style={{ width: "100%" }}>
                      <Input.Password
                        value={pastedKey}
                        placeholder={t("mcp.pasteKey")}
                        onChange={(event) => setPastedKey(event.target.value)}
                      />
                      <Button
                        loading={checking}
                        disabled={!pastedKey.trim() || knowledgeBaseIds.length === 0}
                        onClick={() => void runSelfCheck(pastedKey, knowledgeBaseIds)}
                      >
                        {t("mcp.runSelfCheck")}
                      </Button>
                    </Space.Compact>
                  )}
                  {checks.length > 0 && (
                    <ul className="mcp-check-list">
                      {checks.map((check) => (
                        <li key={check.key}>
                          {checkTag(check.status)} {t(`mcp.checkSteps.${check.key}`)}
                          {check.detail && <Typography.Text type="secondary"> — {check.detail}</Typography.Text>}
                          {typeof check.ms === "number" && check.status !== "skipped" && (
                            <Typography.Text type="secondary"> ({check.ms}ms)</Typography.Text>
                          )}
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              </Flex>
            </Card>
          </Col>
        </Row>

        <Card title={t("mcp.toolsTitle")} extra={<Tag icon={<ApiOutlined />}>{mcpTools.length}</Tag>}>
          <Typography.Paragraph type="secondary">{t("mcp.toolsOrder")}</Typography.Paragraph>
          <Table<McpTool>
            size="middle"
            rowKey="name"
            columns={toolColumns}
            dataSource={[...mcpTools]}
            pagination={false}
            scroll={{ x: true }}
          />
        </Card>

        <Card title={t("mcp.buildTitle")}>
          <Flex vertical gap={16}>
            <div>
              <Typography.Title level={5}>{t("mcp.buildBinary")}</Typography.Title>
              <pre className="ops-report">{t("mcp.buildCommand")}</pre>
            </div>
            <div>
              <Typography.Title level={5}>{t("mcp.releaseTitle")}</Typography.Title>
              <Typography.Paragraph>{t("mcp.releaseHint")}</Typography.Paragraph>
              <Button href={mcpReleaseUrl} target="_blank" rel="noreferrer" icon={<CloudDownloadOutlined />}>
                {t("mcp.releaseLink")}
              </Button>
            </div>
            <div>
              <Typography.Title level={5}>{t("mcp.dockerTitle")}</Typography.Title>
              <pre className="ops-report">{t("mcp.dockerCommand")}</pre>
            </div>
            <div>
              <Typography.Title level={5}>{t("mcp.crossCompile")}</Typography.Title>
              <pre className="ops-report">{t("mcp.crossCompileCommand")}</pre>
            </div>
            <div>
              <Typography.Title level={5}>{t("mcp.selfCheck")}</Typography.Title>
              <ol className="mcp-steps">
                <li>{t("mcp.selfCheckSteps.build")}</li>
                <li>{t("mcp.selfCheckSteps.configure")}</li>
                <li>{t("mcp.selfCheckSteps.call")}</li>
                <li>{t("mcp.selfCheckSteps.negative")}</li>
                <li>{t("mcp.selfCheckSteps.script")}</li>
              </ol>
            </div>
            <Space wrap>
              <Link to="/ops">{t("mcp.manageKeys")}</Link>
              <Typography.Text type="secondary">{t("mcp.revokeNote")}</Typography.Text>
            </Space>
          </Flex>
        </Card>
      </div>

      <Modal
        open={tokenOpen}
        title={t("mcp.tokenTitle")}
        onCancel={() => setTokenOpen(false)}
        onOk={() => setTokenOpen(false)}
        okText={t("mcp.savedToken")}
        cancelButtonProps={{ style: { display: "none" } }}
      >
        <Alert type="warning" showIcon message={t("mcp.tokenOnce")} className="settings-alert" />
        <Typography.Paragraph className="token-box" copyable={{ text: issued?.token ?? "" }}>
          <code>{issued?.token}</code>
        </Typography.Paragraph>
        <Typography.Text type="secondary">{t("mcp.tokenScope", { scope: issued?.knowledgeBaseNames.join(", ") ?? "" })}</Typography.Text>
      </Modal>
    </div>
  );
}
