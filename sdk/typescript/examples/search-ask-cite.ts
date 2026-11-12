import { RagClient } from "../src/index";

const baseUrl = process.env.RAG_API_URL ?? "http://localhost:8082/api/v1";
const apiKey = process.env.RAG_API_KEY;
const knowledgeBaseId = process.env.RAG_KNOWLEDGE_BASE_ID;

if (!apiKey || !knowledgeBaseId) {
  console.error("set RAG_API_KEY and RAG_KNOWLEDGE_BASE_ID");
  process.exit(2);
}

const client = new RagClient({ baseUrl, apiKey });

const knowledgeBases = await client.listKnowledgeBases();
console.log(
  "knowledge bases:",
  (knowledgeBases.items ?? []).map((item) => item.id),
);

const evidence = await client.search({
  query: "hybrid recall",
  knowledgeBaseId,
});
console.log("evidence chunks:", evidence.items?.length ?? 0);

const answer = await client.ask({
  knowledgeBaseId,
  question: "What is hybrid recall?",
});
console.log("answer:", answer.content);
console.log("citations:", answer.citations.length);

const chunkId = answer.citations[0]?.chunkId;
if (chunkId) {
  const chunk = await client.getCitation(chunkId);
  console.log("citation source:", chunk.sourcePath, "content:", chunk.content?.slice(0, 120));
}