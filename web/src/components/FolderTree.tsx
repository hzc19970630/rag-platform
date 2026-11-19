import {
  CheckOutlined,
  CloseOutlined,
  DeleteOutlined,
  DownloadOutlined,
  EditOutlined,
  FolderAddOutlined,
  FolderOutlined,
  MoreOutlined,
  SearchOutlined,
} from "@ant-design/icons";
import { Button, Checkbox, Dropdown, Flex, Input, Space, Tooltip, Tree, Typography } from "antd";
import type { DataNode } from "antd/es/tree";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Folder } from "../api/types";

export const FOLDER_ALL = "all";
export const FOLDER_ROOT = "root";

type Props = {
  folders: Folder[];
  knowledgeBaseName: string;
  selectedKey: string;
  canWrite: boolean;
  canDelete: boolean;
  onSelect: (key: string) => void;
  onCreate: (parentID: string, name: string) => Promise<void>;
  onRename: (folder: Folder) => void;
  onMove: (folder: Folder) => void;
  onDelete: (folder: Folder) => void;
  onDownload: (folders: Folder[]) => void;
  onBatchDelete: (folders: Folder[]) => void;
};

export function FolderTree(props: Props) {
  const { t } = useTranslation();
  const { folders, knowledgeBaseName, selectedKey, canWrite, canDelete, onSelect, onCreate, onRename, onMove, onDelete, onDownload, onBatchDelete } = props;
  const [expandedKeys, setExpandedKeys] = useState<string[]>([FOLDER_ROOT]);
  const [query, setQuery] = useState("");
  const [editing, setEditing] = useState(false);
  const [checkedKeys, setCheckedKeys] = useState<string[]>([]);
  // undefined closes the inline editor; "" creates at the root level.
  const [createParent, setCreateParent] = useState<string | undefined>(undefined);
  const [createName, setCreateName] = useState("");
  const [createBusy, setCreateBusy] = useState(false);

  function startCreate(parentID: string) {
    setQuery("");
    if (parentID) {
      setExpandedKeys((current) => (current.includes(parentID) ? current : [...current, parentID]));
    }
    setCreateName("");
    setCreateParent(parentID);
  }

  function cancelCreate() {
    setCreateParent(undefined);
    setCreateName("");
  }

  async function submitCreate() {
    const name = createName.trim();
    if (!name || createBusy || createParent === undefined) return;
    setCreateBusy(true);
    try {
      await props.onCreate(createParent, name);
      setCreateParent(undefined);
      setCreateName("");
    } catch {
      // The page reports the failure; keep the inline editor open for retry.
    } finally {
      setCreateBusy(false);
    }
  }

  // The folder list arrives asynchronously, so defaultExpandAll would run on an
  // empty tree. Expand every folder whenever the set changes instead.
  useEffect(() => {
    setExpandedKeys((current) => {
      const next = new Set([...current, ...folders.map((folder) => folder.id)]);
      return next.size === current.length ? current : Array.from(next);
    });
    const live = new Set(folders.map((folder) => folder.id));
    setCheckedKeys((current) => {
      const next = current.filter((id) => live.has(id));
      return next.length === current.length ? current : next;
    });
  }, [folders]);

  const treeData = useMemo<DataNode[]>(() => {
    const visible = new Set<string>();
    const needle = query.trim().toLowerCase();
    if (needle) {
      const byID = new Map(folders.map((folder) => [folder.id, folder]));
      for (const folder of folders) {
        if (!folder.name.toLowerCase().includes(needle)) continue;
        visible.add(folder.id);
        let parent = folder.parentId ?? "";
        while (parent) {
          visible.add(parent);
          parent = byID.get(parent)?.parentId ?? "";
        }
      }
    }
    const children = new Map<string, Folder[]>();
    for (const folder of folders) {
      if (needle && !visible.has(folder.id)) continue;
      const parent = folder.parentId ?? "";
      const list = children.get(parent) ?? [];
      list.push(folder);
      children.set(parent, list);
    }
    function createRow(parentID: string): DataNode {
      return {
        key: `create:${parentID || "root"}`,
        selectable: false,
        checkable: false,
        isLeaf: true,
        title: (
          <span className="folder-create-row" onClick={(event) => event.stopPropagation()}>
            <Input
              size="small"
              value={createName}
              placeholder={t("folder.name")}
              aria-label={t("folder.create")}
              onChange={(event) => setCreateName(event.target.value)}
              onPressEnter={() => void submitCreate()}
              onKeyDown={(event) => {
                if (event.key === "Escape") {
                  event.preventDefault();
                  cancelCreate();
                }
              }}
            />
            <Tooltip title={t("common.confirm")}>
              <Button
                size="small"
                type="text"
                icon={<CheckOutlined />}
                aria-label={t("common.confirm")}
                loading={createBusy}
                disabled={!createName.trim()}
                onClick={() => void submitCreate()}
              />
            </Tooltip>
            <Tooltip title={t("common.cancel")}>
              <Button
                size="small"
                type="text"
                icon={<CloseOutlined />}
                aria-label={t("common.cancel")}
                onClick={cancelCreate}
              />
            </Tooltip>
          </span>
        ),
      };
    }
    function build(parentID: string): DataNode[] {
      const nodes: DataNode[] = (children.get(parentID) ?? [])
        .sort((left, right) => left.name.localeCompare(right.name))
        .map((folder) => ({
          key: folder.id,
          title: (
            <span className="folder-title">
              <span className="folder-title-name">{folder.name}</span>
              {canWrite && !editing && (
                <span className="folder-actions">
                  <Tooltip title={t("folder.createChild")}>
                    <Button
                      type="text"
                      size="small"
                      icon={<FolderAddOutlined />}
                      aria-label={t("folder.createChild")}
                      onClick={(event) => { event.stopPropagation(); startCreate(folder.id); }}
                    />
                  </Tooltip>
                  <Dropdown
                    trigger={["click"]}
                    menu={{
                      items: [
                        { key: "rename", icon: <EditOutlined />, label: t("folder.rename") },
                        { key: "move", icon: <FolderOutlined />, label: t("folder.move") },
                        { key: "delete", icon: <DeleteOutlined />, label: t("folder.delete"), danger: true },
                      ],
                      onClick: ({ key, domEvent }) => {
                        domEvent.stopPropagation();
                        if (key === "rename") onRename(folder);
                        if (key === "move") onMove(folder);
                        if (key === "delete") onDelete(folder);
                      },
                    }}
                  >
                    <Button
                      type="text"
                      size="small"
                      icon={<MoreOutlined />}
                      aria-label={t("folder.actions")}
                      onClick={(event) => event.stopPropagation()}
                    />
                  </Dropdown>
                </span>
              )}
            </span>
          ),
          children: build(folder.id),
        }));
      if (createParent === parentID) nodes.unshift(createRow(parentID));
      return nodes;
    }
    const allNode: DataNode = {
      key: FOLDER_ALL,
      title: t("folder.all"),
      selectable: !editing,
      disableCheckbox: true,
    };
    const rootNode: DataNode = {
      key: FOLDER_ROOT,
      title: (
        <span className="folder-title">
          <span className="folder-title-name">{knowledgeBaseName}</span>
          {canWrite && !editing && (
            <span className="folder-actions">
              <Tooltip title={t("folder.createChild")}>
                <Button
                  type="text"
                  size="small"
                  icon={<FolderAddOutlined />}
                  aria-label={t("folder.createChild")}
                  onClick={(event) => { event.stopPropagation(); startCreate(""); }}
                />
              </Tooltip>
            </span>
          )}
        </span>
      ),
      selectable: !editing,
      disableCheckbox: true,
      children: build(""),
    };
    return [allNode, rootNode];
  }, [folders, knowledgeBaseName, query, editing, canWrite, createParent, createName, createBusy, props.onCreate, onDelete, onMove, onRename, t]);

  const parentID = selectedKey === FOLDER_ALL || selectedKey === FOLDER_ROOT ? "" : selectedKey;
  const selectedFolders = folders.filter((folder) => checkedKeys.includes(folder.id));
  const allChecked = folders.length > 0 && selectedFolders.length === folders.length;
  const partiallyChecked = selectedFolders.length > 0 && !allChecked;

  function exitEditing() {
    setEditing(false);
    setCheckedKeys([]);
  }

  return (
    <div className="folder-panel">
      {editing ? (
        <Flex className="folder-panel-head folder-panel-head-edit" align="center" justify="space-between" gap={8} wrap="wrap">
          <Checkbox
            checked={allChecked}
            indeterminate={partiallyChecked}
            onChange={(event) => setCheckedKeys(event.target.checked ? folders.map((folder) => folder.id) : [])}
          >
            {t("folder.selectedCount", { count: selectedFolders.length })}
          </Checkbox>
          <Space size={4} wrap>
            <Tooltip title={t("folder.download")}>
              <Button
                size="small"
                icon={<DownloadOutlined />}
                aria-label={t("folder.download")}
                disabled={selectedFolders.length === 0}
                onClick={() => onDownload(selectedFolders)}
              />
            </Tooltip>
            {canDelete && (
              <Tooltip title={t("folder.batchDelete")}>
                <Button
                  size="small"
                  danger
                  icon={<DeleteOutlined />}
                  aria-label={t("folder.batchDelete")}
                  disabled={selectedFolders.length === 0}
                  onClick={() => onBatchDelete(selectedFolders)}
                />
              </Tooltip>
            )}
            <Tooltip title={t("folder.exitEdit")}>
              <Button size="small" type="text" icon={<CloseOutlined />} aria-label={t("folder.exitEdit")} onClick={exitEditing} />
            </Tooltip>
          </Space>
        </Flex>
      ) : (
        <div className="folder-panel-head">
          <Typography.Text strong>{t("folder.title")}</Typography.Text>
          <Space size={4}>
            <Input
              size="small"
              allowClear
              className="folder-search"
              prefix={<SearchOutlined />}
              placeholder={t("folder.search")}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
            {canWrite && (
              <Tooltip title={t("folder.create")}>
                <Button
                  type="text"
                  size="small"
                  icon={<FolderAddOutlined />}
                  aria-label={t("folder.create")}
                  onClick={() => startCreate(parentID)}
                />
              </Tooltip>
            )}
            <Tooltip title={t("folder.edit")}>
              <Button size="small" type="text" icon={<EditOutlined />} aria-label={t("folder.edit")} onClick={() => setEditing(true)} />
            </Tooltip>
          </Space>
        </div>
      )}
      {query.trim() !== "" && treeData.length <= 2 && (
        <Typography.Paragraph type="secondary" className="folder-search-empty">{t("folder.noMatch")}</Typography.Paragraph>
      )}
      <div className="folder-tree-scroll">
        <Tree
          blockNode
          treeData={treeData}
          expandedKeys={expandedKeys}
          onExpand={(keys) => setExpandedKeys(keys.map(String))}
          checkable={editing}
          checkedKeys={checkedKeys}
          onCheck={(keys) => setCheckedKeys((Array.isArray(keys) ? keys : keys.checked).map(String))}
          selectable={!editing}
          selectedKeys={editing ? [] : [selectedKey]}
          onSelect={(keys) => onSelect(String(keys[0] ?? FOLDER_ALL))}
        />
      </div>
    </div>
  );
}