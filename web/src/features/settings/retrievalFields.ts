import type { IndexingStrategy } from "../../api/types";

// Required capabilities are the query requirements for this knowledge base.
// Evidence is always required; a projection capability is only kept while its
// switch is enabled. Enabling wiki/graph does not silently make every query
// depend on that projection, which keeps capability failures isolated.
export function normalizedStrategy(strategy: IndexingStrategy): IndexingStrategy {
  const capabilities = new Set(strategy.required_capabilities.length > 0 ? strategy.required_capabilities : ["evidence"]);
  capabilities.add("evidence");
  if (!strategy.wiki_enabled) capabilities.delete("wiki");
  if (!strategy.graph_enabled) {
    capabilities.delete("graph");
    capabilities.delete("relation");
  }
  return { ...strategy, required_capabilities: Array.from(capabilities) };
}
