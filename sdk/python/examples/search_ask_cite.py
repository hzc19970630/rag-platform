import os

from rag_platform import RagClient

base_url = os.getenv("RAG_API_URL", "http://localhost:8082/api/v1")
api_key = os.getenv("RAG_API_KEY")
knowledge_base_id = os.getenv("RAG_KNOWLEDGE_BASE_ID")

if not api_key or not knowledge_base_id:
    raise SystemExit("set RAG_API_KEY and RAG_KNOWLEDGE_BASE_ID")

with RagClient(base_url, api_key) as client:
    knowledge_bases = client.list_knowledge_bases()
    print("knowledge bases:", [item.get("id") for item in knowledge_bases.get("items", [])])

    evidence = client.search("hybrid recall", knowledge_base_id)
    print("evidence chunks:", len(evidence.get("items", [])))

    answer = client.ask(
        "What is hybrid recall?",
        knowledge_base_id=knowledge_base_id,
    )
    print("answer:", answer.content)
    print("citations:", len(answer.citations))

    if answer.citations:
        chunk_id = answer.citations[0].get("chunkId")
        if chunk_id:
            chunk = client.get_citation(chunk_id)
            print("citation source:", chunk.get("sourcePath"), "content:", (chunk.get("content") or "")[:120])