import { Alert, App, Button, Card, Descriptions, Flex, Input, Modal, Popconfirm, Space, Table, Tag, Typography, type TableColumnsType } from "antd";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api, jsonInit } from "../../api/client";
import { usePresets } from "../../api/hooks";
import type { PresetDetail, PresetSummary } from "../../api/types";

const emptyManifest = {
  schemaVersion: "EnterprisePresetV1",
  name: "custom-pack",
  version: "1.0.0",
  displayName: "自定义预置包",
  description: "",
  vertical: "general",
  retrieval: { top_k: 8 },
  chunking: { size: 800, overlap: 80, min_size: 40, split_markers: ["\n\n", "\n"] },
  formats: ["markdown", "txt"],
};

export function PresetTab() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const presets = usePresets();
  const [editorOpen, setEditorOpen] = useState(false);
  const [editorText, setEditorText] = useState("");
  const [editing, setEditing] = useState<PresetSummary | null>(null);
  const [cloneOpen, setCloneOpen] = useState(false);
  const [cloneSource, setCloneSource] = useState<PresetSummary | null>(null);
  const [cloneName, setCloneName] = useState("");
  const [cloneVersion, setCloneVersion] = useState("1.0.0");
  const [detail, setDetail] = useState<PresetDetail | null>(null);
  const [busy, setBusy] = useState(false);

  async function reload() {
    await presets.refetch();
  }

  function openCreate() {
    setEditing(null);
    setEditorText(JSON.stringify(emptyManifest, null, 2));
    setEditorOpen(true);
  }

  async function openEdit(row: PresetSummary) {
    setBusy(true);
    try {
      const loaded = await api<PresetDetail>(`/presets/${row.name}/${row.version}`);
      setEditing(row);
      setEditorText(JSON.stringify(loaded.manifest, null, 2));
      setEditorOpen(true);
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("presets.loadFailed"));
    } finally {
      setBusy(false);
    }
  }

  async function saveEditor() {
    setBusy(true);
    try {
      const manifest = JSON.parse(editorText) as Record<string, unknown>;
      if (editing) {
        await api(`/presets/${editing.name}/${editing.version}`, jsonInit({ manifest }, "PUT"));
      } else {
        await api("/presets", jsonInit({ manifest }));
      }
      message.success(t("presets.saved"));
      setEditorOpen(false);
      await reload();
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("presets.saveFailed"));
    } finally {
      setBusy(false);
    }
  }

  async function view(row: PresetSummary) {
    setBusy(true);
    try {
      setDetail(await api<PresetDetail>(`/presets/${row.name}/${row.version}`));
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("presets.loadFailed"));
    } finally {
      setBusy(false);
    }
  }

  async function clone() {
    if (!cloneSource) return;
    setBusy(true);
    try {
      await api(`/presets/${cloneSource.name}/${cloneSource.version}/clone`, jsonInit({ name: cloneName, version: cloneVersion }));
      message.success(t("presets.cloned"));
      setCloneOpen(false);
      await reload();
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("presets.saveFailed"));
    } finally {
      setBusy(false);
    }
  }

  async function remove(row: PresetSummary) {
    setBusy(true);
    try {
      await api(`/presets/${row.name}/${row.version}`, { method: "DELETE" });
      message.success(t("presets.deleted"));
      await reload();
    } catch (cause) {
      message.error(cause instanceof Error ? cause.message : t("presets.saveFailed"));
    } finally {
      setBusy(false);
    }
  }

  const columns: TableColumnsType<PresetSummary> = [
    {
      title: t("presets.name"), dataIndex: "displayName",
      render: (value: string, row) => (
        <Space>
          <Typography.Text strong>{value}</Typography.Text>
          <Typography.Text code>{`${row.name}@${row.version}`}</Typography.Text>
        </Space>
      ),
    },
    {
      title: t("presets.source"), dataIndex: "source", width: 100,
      render: (value: string) => <Tag color={value === "builtin" ? "blue" : "green"}>{value === "builtin" ? t("presets.builtin") : t("presets.tenant")}</Tag>,
    },
    { title: t("presets.vertical"), dataIndex: "vertical", width: 110 },
    { title: t("presets.formats"), dataIndex: "formats", render: (values: string[]) => values.join(", ") },
    {
      title: t("presets.actions"), key: "actions", width: 320,
      render: (_, row) => (
        <Space wrap>
          <Button size="small" onClick={() => void view(row)}>{t("presets.view")}</Button>
          <Button size="small" onClick={() => { setCloneSource(row); setCloneName(`${row.name}-custom`); setCloneVersion("1.0.0"); setCloneOpen(true); }}>{t("presets.clone")}</Button>
          {row.source === "tenant" ? <Button size="small" onClick={() => void openEdit(row)}>{t("presets.edit")}</Button> : null}
          {row.source === "tenant" ? (
            <Popconfirm title={t("presets.deleteConfirm")} onConfirm={() => void remove(row)}>
              <Button size="small" danger>{t("presets.delete")}</Button>
            </Popconfirm>
          ) : null}
        </Space>
      ),
    },
  ];

  return (
    <Card
      title={t("presets.title")}
      extra={<Space><Button size="small" onClick={() => void reload()}>{t("common.refresh")}</Button><Button type="primary" size="small" onClick={openCreate}>{t("presets.import")}</Button></Space>}
    >
      <Flex vertical gap={12}>
        <Alert type="info" showIcon message={t("presets.hint")} />
        <Table
          rowKey={(row) => `${row.name}@${row.version}`}
          columns={columns}
          dataSource={presets.data?.items ?? []}
          loading={presets.isLoading || busy}
          pagination={false}
          size="small"
          scroll={{ x: 900 }}
        />
      </Flex>

      <Modal open={editorOpen} title={editing ? t("presets.editTitle") : t("presets.importTitle")} width={780} confirmLoading={busy} onOk={() => void saveEditor()} onCancel={() => setEditorOpen(false)} okText={t("common.save")}>
        <Input.TextArea value={editorText} onChange={(event) => setEditorText(event.target.value)} rows={18} className="preset-editor" spellCheck={false} />
      </Modal>

      <Modal open={cloneOpen} title={t("presets.cloneTitle")} confirmLoading={busy} onOk={() => void clone()} onCancel={() => setCloneOpen(false)} okText={t("presets.clone")} okButtonProps={{ disabled: !cloneName.trim() || !cloneVersion.trim() }}>
        <Flex vertical gap={12}>
          <label className="field-label">{t("presets.cloneName")}<Input value={cloneName} onChange={(event) => setCloneName(event.target.value)} /></label>
          <label className="field-label">{t("presets.cloneVersion")}<Input value={cloneVersion} onChange={(event) => setCloneVersion(event.target.value)} /></label>
        </Flex>
      </Modal>

      <Modal open={Boolean(detail)} title={t("presets.viewTitle")} width={780} footer={<Button onClick={() => setDetail(null)}>{t("common.close")}</Button>} onCancel={() => setDetail(null)}>
        {detail ? (
          <Flex vertical gap={12}>
            <Descriptions size="small" column={2}>
              <Descriptions.Item label={t("presets.source")}>{detail.source === "builtin" ? t("presets.builtin") : t("presets.tenant")}</Descriptions.Item>
              <Descriptions.Item label={t("presets.vertical")}>{String(detail.manifest.vertical ?? "")}</Descriptions.Item>
            </Descriptions>
            <Typography.Paragraph type="secondary">{String(detail.manifest.description ?? "")}</Typography.Paragraph>
            <Typography.Text strong>{t("presets.defaults")}</Typography.Text>
            <pre className="preset-json">{JSON.stringify(detail.manifest, null, 2)}</pre>
          </Flex>
        ) : null}
      </Modal>
    </Card>
  );
}
