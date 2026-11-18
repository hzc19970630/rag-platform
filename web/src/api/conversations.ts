import { api, jsonInit } from "./client";
import type { Conversation } from "./types";

// StrictMode mounts effects twice in development; sharing the in-flight
// creation keeps a knowledge-base entry point from opening two sessions.
const sessionCreations = new Map<string, Promise<Conversation>>();

export function createConversation(knowledgeBaseId: string): Promise<Conversation> {
  const pending = sessionCreations.get(knowledgeBaseId);
  if (pending) return pending;
  const request = api<Conversation>("/sessions", jsonInit({ knowledge_base_id: knowledgeBaseId }))
    .finally(() => sessionCreations.delete(knowledgeBaseId));
  sessionCreations.set(knowledgeBaseId, request);
  return request;
}
