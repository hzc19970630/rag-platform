import { Alert, Button, Flex, InputNumber, Select, Space, Tag, Typography } from "antd";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { ConfigFieldDefinition, ConfigFieldState } from "../../api/types";
import {
  applyModeLabelKey,
  buildOverride,
  displayValue,
  fieldError,
  groupLabelKey,
  initialValues,
  isDirty,
  isOverridden,
  sourceLabelKey,
  type ConfigValue,
  type SchemaDomain,
} from "./schemaForm";

type Props = {
  domain: SchemaDomain;
  override: Record<string, unknown>;
  titleKey: string;
  hintKey?: string;
  canWrite: boolean;
  saving: boolean;
  onSave: (override: Record<string, unknown>) => Promise<void>;
  onClear: () => Promise<void>;
};

// SchemaConfigForm renders every field from the server schema. It has no
// hand-written parameter list, so a new retrieval or chunking parameter appears
// in the workbench as soon as the server declares it.
export function SchemaConfigForm({ domain, override, titleKey, hintKey, canWrite, saving, onSave, onClear }: Props) {
  const { t } = useTranslation();
  const [values, setValues] = useState<Record<string, ConfigValue>>(() => initialValues(domain, override));
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState("");

  useEffect(() => {
    setValues(initialValues(domain, override));
    setFieldErrors({});
    setFormError("");
  }, [domain, override]);

  const groups = useMemo(() => {
    const seen = new Set<string>();
    return domain.schema.filter((field) => {
      if (seen.has(field.group)) return false;
      seen.add(field.group);
      return true;
    }).map((field) => field.group);
  }, [domain.schema]);

  const dirty = isDirty(domain, override, values);
  const hasOverride = Object.keys(override).length > 0;

  function errorText(field: ConfigFieldDefinition): string {
    switch (fieldError(field, values[field.key] ?? null)) {
      case "range":
        return t("retrieval.validationRange", { min: field.min ?? 0, max: field.max ?? "∞" });
      case "positive":
        return t("retrieval.validationPositive");
      case "integer":
        return t("retrieval.validationInteger");
      case "empty":
        return t("configuration.validationEmpty");
      default:
        return "";
    }
  }

  async function save() {
    const errors: Record<string, string> = {};
    for (const field of domain.schema) {
      const text = errorText(field);
      if (text) errors[field.key] = text;
    }
    setFieldErrors(errors);
    if (Object.keys(errors).length > 0) return;
    setFormError("");
    try {
      await onSave(buildOverride(domain, values));
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : t("settings.saveFailed"));
    }
  }

  async function clear() {
    setFormError("");
    try {
      await onClear();
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : t("settings.saveFailed"));
    }
  }

  return (
    <section className="settings-block">
      <header className="settings-block-heading">
        <div>
          <Typography.Title level={4}>{t(titleKey)}</Typography.Title>
          {hintKey && <Typography.Text type="secondary">{t(hintKey)}</Typography.Text>}
        </div>
      </header>
      {!canWrite && <Alert type="info" showIcon message={t("settings.readOnlyHint")} className="settings-alert" />}
      {groups.map((group) => (
        <div className="setting-group" key={group}>
          <Typography.Title level={5}>{t(groupLabelKey(group))}</Typography.Title>
          {domain.schema.filter((field) => field.group === group).map((field) => (
            <ConfigFieldRow
              key={field.key}
              field={field}
              value={values[field.key] ?? null}
              state={domain.fields?.[field.key]}
              overridden={isOverridden(override, field.key) || (values[field.key] !== null && values[field.key] !== undefined)}
              canWrite={canWrite}
              error={fieldErrors[field.key]}
              onChange={(next) => setValues((current) => ({ ...current, [field.key]: next }))}
              onClear={() => setValues((current) => ({ ...current, [field.key]: null }))}
            />
          ))}
        </div>
      ))}
      {formError && <Alert type="error" showIcon message={formError} className="settings-alert" />}
      <Flex justify="space-between" gap={8} className="settings-actions" wrap="wrap">
        <div>
          {canWrite && hasOverride && (
            <Button onClick={() => void clear()} loading={saving}>{t("configuration.clearDomain")}</Button>
          )}
        </div>
        <Button type="primary" onClick={() => void save()} loading={saving} disabled={!canWrite || !dirty}>
          {t("retrieval.retrievalSave")}
        </Button>
      </Flex>
    </section>
  );
}

function ConfigFieldRow(props: {
  field: ConfigFieldDefinition;
  value: ConfigValue;
  state?: ConfigFieldState;
  overridden: boolean;
  canWrite: boolean;
  error?: string;
  onChange: (value: ConfigValue) => void;
  onClear: () => void;
}) {
  const { t } = useTranslation();
  const { field, value, state } = props;
  const effective = state?.value ?? field.default;
  const inherited = state?.value !== undefined ? state.value : field.default;
  return (
    <div className={`setting-row${props.error ? " setting-row-error" : ""}`}>
      <div className="setting-label">
        <Space size={8} wrap>
          <Typography.Text strong>{t(field.labelKey)}</Typography.Text>
          <Tag color={props.overridden ? "green" : "default"}>{props.overridden ? t("common.override") : t("common.inherit")}</Tag>
          {state && <Tag>{t(sourceLabelKey(state.source))}</Tag>}
        </Space>
        <div className="setting-help">
          {t("common.effective")}: {displayValue(effective)}{field.unitKey ? ` ${t(field.unitKey)}` : ""}
          {" · "}{t("configuration.schemaDefault")}: {displayValue(field.default)}
          {field.min !== undefined ? ` · ${field.min}${field.max !== undefined ? `–${field.max}` : "+"}` : ""}
          {state ? ` · ${t(applyModeLabelKey(state.applyMode))}` : ""}
        </div>
        {props.overridden && <div className="setting-help">{t("retrieval.overrideHint")}</div>}
      </div>
      <div className="setting-control">
        <Space size={4} wrap>
          {field.kind === "bool" ? (
            <Select
              value={value === null ? "" : String(value)}
              disabled={!props.canWrite}
              aria-label={t(field.labelKey)}
              style={{ minWidth: 160 }}
              onChange={(next) => props.onChange(next === "" ? null : next === "true")}
              options={[
                { value: "", label: `${t("common.inherit")} (${displayValue(inherited)})` },
                { value: "true", label: "true" },
                { value: "false", label: "false" },
              ]}
            />
          ) : field.kind === "string_list" ? (
            <Select
              mode="tags"
              value={Array.isArray(value) ? value : []}
              disabled={!props.canWrite}
              aria-label={t(field.labelKey)}
              style={{ minWidth: 320 }}
              placeholder={t("common.inherit")}
              onChange={(next: string[]) => props.onChange(next)}
              options={[]}
            />
          ) : (
            <InputNumber
              value={typeof value === "number" ? value : null}
              min={field.min}
              max={field.max}
              step={field.step ?? 1}
              precision={field.kind === "int" ? 0 : undefined}
              placeholder={displayValue(inherited)}
              disabled={!props.canWrite}
              status={props.error ? "error" : undefined}
              aria-label={t(field.labelKey)}
              onChange={(next) => props.onChange(typeof next === "number" ? next : null)}
            />
          )}
          {field.unitKey && <Typography.Text type="secondary">{t(field.unitKey)}</Typography.Text>}
          {props.canWrite && props.overridden && (
            <Button type="link" size="small" onClick={props.onClear}>{t("retrieval.clearOverride")}</Button>
          )}
        </Space>
        {props.error && <div className="field-error" role="alert">{props.error}</div>}
      </div>
    </div>
  );
}
