import { ApiOutlined, PlusOutlined } from "@ant-design/icons";
import {
  Alert,
  App,
  Button,
  Card,
  Col,
  Flex,
  Input,
  Modal,
  Row,
  Select,
  Space,
  Table,
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

export const mcpCapabilities = ["knowledge_base:read", "chat"] as const;

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

export function mcpKeyRequest(name: string, knowledgeBaseId: string): APIKeyCreateRequest {
  return {
    name,
    capabilities: [...mcpCapabilities],
    knowledgeBaseIds: [knowledgeBaseId],
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

type IssuedKey = {
  token: string;
  name: string;
  knowledgeBaseId: string;
  knowledgeBaseName: string;
};

export function McpPage() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const { canAdmin } = usePermissions();
  const knowledgeBases = useKnowledgeBases();

  const [knowledgeBaseId, setKnowledgeBaseId] = useState<string>();
  const [keyName, setKeyName] = useState("");
  const [creating, setCreating] = useState(false);
  const [issued, setIssued] = useState<IssuedKey>();
  const [tokenOpen, setTokenOpen] = useState(false);
  const [apiUrl, setApiUrl] = useState(apiBase);
  const [command, setCommand] = useState("rag-mcp");
  const firstKnowledgeBaseId = knowledgeBases.data?.[0]?.id;

  useEffect(() => {
    if (!knowledgeBaseId && firstKnowledgeBaseId) setKnowledgeBaseId(firstKnowledgeBaseId);
  }, [knowledgeBaseId, firstKnowledgeBaseId]);

  if (!canAdmin) {
    return (
      <div className="page">
        <Alert type="error" showIcon message={t("common.noPermission")} description={t("mcp.noPermission")} />
      </div>
    );
  }

  const selectedKnowledgeBase = knowledgeBases.data?.find((kb) => kb.id === knowledgeBaseId);
  const effectiveName = keyName.trim() || (selectedKnowledgeBase ? `mcp-${selectedKnowledgeBase.name}` : "");
  const clientConfig = buildMcpClientConfig({
    apiUrl: apiUrl.trim() || apiBase,
    apiKey: issued?.token ?? "${RAG_API_KEY}",
    command: command.trim() || "rag-mcp",
  });

  async function createKey() {
    if (!knowledgeBaseId) {
      message.warning(t("mcp.selectKnowledgeBaseFirst"));
      return;
    }
    setCreating(true);
    try {
      const result = await api<APIKeyCreateResponse>("/api-keys", jsonInit(mcpKeyRequest(effectiveName, knowledgeBaseId)));
      setIssued({
        token: result.token,
        name: effectiveName,
        knowledgeBaseId,
        knowledgeBaseName: selectedKnowledgeBase?.name ?? knowledgeBaseId,
      });
      setTokenOpen(true);
      await queryClient.invalidateQueries({ queryKey: queryKeys.apiKeys });
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("mcp.createKeyFailed"));
    } finally {
      setCreating(false);
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
        <Tag color={row.access === "read" ? "green" : "gold"}>
          {t(`mcp.access.${row.access}`)}
        </Tag>
      ),
    },
    {
      title: t("mcp.toolDescriptionColumn"),
      key: "description",
      render: (_, row) => t(`mcp.tools.${row.name}`),
    },
  ];

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
                  <Typography.Text strong>{t("mcp.knowledgeBase")}</Typography.Text>
                  <Select
                    className="mcp-field"
                    showSearch
                    optionFilterProp="label"
                    loading={knowledgeBases.isLoading}
                    value={knowledgeBaseId}
                    placeholder={t("mcp.knowledgeBasePlaceholder")}
                    options={knowledgeBaseOptions}
                    onChange={setKnowledgeBaseId}
                  />
                </div>
                <div>
                  <Typography.Text strong>{t("mcp.keyName")}</Typography.Text>
                  <Input
                    className="mcp-field"
                    value={keyName}
                    placeholder={selectedKnowledgeBase ? `mcp-${selectedKnowledgeBase.name}` : t("mcp.keyNamePlaceholder")}
                    onChange={(event) => setKeyName(event.target.value)}
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
                  disabled={!knowledgeBaseId}
                  onClick={() => void createKey()}
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
                    message={t("mcp.keyIssued", { name: issued.name, knowledgeBase: issued.knowledgeBaseName })}
                  />
                ) : (
                  <Alert type="warning" showIcon message={t("mcp.keyRequiredForConfig")} />
                )}
                <Typography.Paragraph className="token-box" copyable={{ text: clientConfig }}>
                  <pre className="ops-report">{clientConfig}</pre>
                </Typography.Paragraph>
                <Typography.Text type="secondary">{t("mcp.clientLocations")}</Typography.Text>
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
        <Typography.Text type="secondary">
          {t("mcp.tokenScope", { knowledgeBase: issued?.knowledgeBaseName ?? "" })}
        </Typography.Text>
      </Modal>
    </div>
  );
}
