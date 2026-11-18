import { DeleteOutlined, EditOutlined, FolderAddOutlined, FolderOutlined, MoreOutlined } from "@ant-design/icons";
import { Button, Dropdown, Tree, Typography } from "antd";
import type { DataNode } from "antd/es/tree";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Folder } from "../api/types";

export const FOLDER_ALL = "all";
export const FOLDER_ROOT = "root";

type Props = {
  folders: Folder[];
  selectedKey: string;
  canWrite: boolean;
  onSelect: (key: string) => void;
  onCreate: (parentID: string) => void;
  onRename: (folder: Folder) => void;
  onMove: (folder: Folder) => void;
  onDelete: (folder: Folder) => void;
};

export function FolderTree({ folders, selectedKey, canWrite, onSelect, onCreate, onRename, onMove, onDelete }: Props) {
  const { t } = useTranslation();
  const [expandedKeys, setExpandedKeys] = useState<string[]>([]);
  // The folder list arrives asynchronously, so defaultExpandAll would run on an
  // empty tree. Expand every folder whenever the set changes instead.
  useEffect(() => {
    setExpandedKeys((current) => Array.from(new Set([...current, ...folders.map((folder) => folder.id)])));
  }, [folders]);
  const treeData = useMemo<DataNode[]>(() => {
    const children = new Map<string, Folder[]>();
    for (const folder of folders) {
      const parent = folder.parentId ?? "";
      const list = children.get(parent) ?? [];
      list.push(folder);
      children.set(parent, list);
    }
    function build(parentID: string): DataNode[] {
      return (children.get(parentID) ?? [])
        .sort((left, right) => left.name.localeCompare(right.name))
        .map((folder) => ({
          key: folder.id,
          title: (
            <span className="folder-title">
              <span className="folder-title-name">{folder.name}</span>
              {canWrite && (
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
              )}
            </span>
          ),
          children: build(folder.id),
        }));
    }
    return [
      { key: FOLDER_ALL, title: t("folder.all"), selectable: true },
      { key: FOLDER_ROOT, title: t("folder.root"), selectable: true },
      ...build(""),
    ];
  }, [folders, canWrite, onDelete, onMove, onRename, t]);

  const parentID = selectedKey === FOLDER_ALL || selectedKey === FOLDER_ROOT ? "" : selectedKey;

  return (
    <div className="folder-panel">
      <div className="folder-panel-head">
        <Typography.Text strong>{t("folder.title")}</Typography.Text>
        {canWrite && (
          <Button size="small" icon={<FolderAddOutlined />} onClick={() => onCreate(parentID)}>
            {t("folder.create")}
          </Button>
        )}
      </div>
      <Tree
        blockNode
        treeData={treeData}
        expandedKeys={expandedKeys}
        onExpand={(keys) => setExpandedKeys(keys.map(String))}
        selectedKeys={[selectedKey]}
        onSelect={(keys) => onSelect(String(keys[0] ?? FOLDER_ALL))}
      />
    </div>
  );
}