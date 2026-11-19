import { TreeSelect } from "antd";
import { useEffect, useMemo, useState, type CSSProperties } from "react";
import type { Folder } from "../api/types";
import { FOLDER_ROOT } from "./FolderTree";

type Props = {
  folders: Folder[];
  knowledgeBaseName: string;
  value?: string;
  onChange: (value: string) => void;
  disabledValues?: string[];
  placeholder?: string;
  ariaLabel?: string;
  style?: CSSProperties;
};

type TreeNode = {
  value: string;
  title: string;
  disabled?: boolean;
  children?: TreeNode[];
};

// Same hierarchy as the knowledge base folder tree: the knowledge base itself
// is the root node, real folders nest below it.
export function FolderTreeSelect(props: Props) {
  const { folders, knowledgeBaseName, value, onChange, disabledValues, placeholder, ariaLabel, style } = props;
  const [expandedKeys, setExpandedKeys] = useState<string[]>([]);

  const allKeys = useMemo(() => [FOLDER_ROOT, ...folders.map((folder) => folder.id)], [folders]);
  useEffect(() => {
    setExpandedKeys(allKeys);
  }, [allKeys]);

  const disabled = useMemo(() => new Set(disabledValues ?? []), [disabledValues]);

  const treeData = useMemo<TreeNode[]>(() => {
    const children = new Map<string, Folder[]>();
    for (const folder of folders) {
      const parent = folder.parentId ?? "";
      const list = children.get(parent) ?? [];
      list.push(folder);
      children.set(parent, list);
    }
    function build(parentID: string): TreeNode[] {
      return (children.get(parentID) ?? [])
        .sort((left, right) => left.name.localeCompare(right.name))
        .map((folder) => {
          const nodes = build(folder.id);
          return {
            value: folder.id,
            title: folder.name,
            disabled: disabled.has(folder.id),
            ...(nodes.length > 0 ? { children: nodes } : {}),
          };
        });
    }
    const nodes = build("");
    return [{
      value: FOLDER_ROOT,
      title: knowledgeBaseName,
      ...(nodes.length > 0 ? { children: nodes } : {}),
    }];
  }, [folders, knowledgeBaseName, disabled]);

  return (
    <TreeSelect
      treeData={treeData}
      value={value}
      onChange={(next) => { if (next) onChange(String(next)); }}
      treeExpandedKeys={expandedKeys}
      onTreeExpand={(keys) => setExpandedKeys(keys.map(String))}
      showSearch
      treeNodeFilterProp="title"
      placeholder={placeholder}
      aria-label={ariaLabel}
      style={style}
      popupMatchSelectWidth={false}
      data-testid="folder-tree-select"
    />
  );
}