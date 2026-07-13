import type { Provider } from "../types";

import anthropicCredential from "../assets/n8n/anthropic-credential.png";
import geminiCredential from "../assets/n8n/gemini-credential.png";
import openaiCredential from "../assets/n8n/openai-credential.png";
import openaiNodeCredential from "../assets/n8n/openai-node-credential.png";

export interface N8nSetupGuide {
  credentialLabel: string;
  nodeLabel: string;
  urlField?: "Base URL" | "Host";
  credentialImage?: string;
  nodeImage?: string;
  steps: string[];
  note?: string;
}

export function n8nSetupGuide(provider: Provider, baseUrl: string): N8nSetupGuide | null {
  switch (provider) {
    case "openai":
      return {
        credentialLabel: "OpenAI",
        nodeLabel: "OpenAI Chat Model",
        urlField: "Base URL",
        credentialImage: openaiCredential,
        nodeImage: openaiNodeCredential,
        steps: [
          "Credentials → Add credential → OpenAI.",
          `Set Base URL to ${baseUrl} (no trailing slash).`,
          "Paste your iw: proxy key in API Key.",
          "In your workflow, add an OpenAI Chat Model node and pick that credential.",
        ],
      };
    case "anthropic":
      return {
        credentialLabel: "Anthropic",
        nodeLabel: "Anthropic Chat Model",
        urlField: "Base URL",
        credentialImage: anthropicCredential,
        steps: [
          "Credentials → Add credential → Anthropic API.",
          `Set Base URL to ${baseUrl} (no trailing slash).`,
          "Paste your iw: proxy key in API Key.",
          "In your workflow, add an Anthropic Chat Model node and pick that credential.",
          "The credential test may fail even when workflows work — try a live run if Retry errors.",
        ],
      };
    case "gemini":
      return {
        credentialLabel: "Google Gemini (PaLM) API",
        nodeLabel: "Google Gemini Chat Model",
        urlField: "Host",
        credentialImage: geminiCredential,
        steps: [
          "Credentials → Add credential → Google Gemini(PaLM) API.",
          `Set Host to ${baseUrl} (no trailing slash).`,
          "Paste your iw: proxy key in API Key.",
          "In your workflow, add a Google Gemini Chat Model node and pick that credential.",
        ],
      };
    case "bedrock":
      return {
        credentialLabel: "AWS Bedrock",
        nodeLabel: "AWS Bedrock Chat Model",
        steps: [
          "The native AWS Bedrock node signs requests with IAM and has no custom endpoint field.",
          "It cannot route through this proxy's SigV4 passthrough without custom Code/HTTP nodes.",
          "For n8n, use OpenAI, Anthropic, or Gemini credentials pointed at the proxy instead.",
          "For Bedrock Mantle (GPT on Bedrock), use an OpenAI credential with Base URL set to",
          `${baseUrl}/bedrock-mantle/openai/v1 (GPT) or ${baseUrl}/bedrock-mantle/anthropic (Claude) with a bedrock-provider iw: proxy key.`,
        ],
        note: "Bedrock via llm-proxy requires client-side SigV4 URL rewriting — not supported by n8n's built-in Bedrock node.",
      };
    default:
      return null;
  }
}
