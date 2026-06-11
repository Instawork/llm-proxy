import Anthropic from "@anthropic-ai/sdk";

const baseURL = process.env.PROXY_BASE_URL;
const apiKey = process.env.PROXY_API_KEY;
if (!baseURL || !apiKey) {
  console.error("PROXY_BASE_URL and PROXY_API_KEY required");
  process.exit(1);
}

const client = new Anthropic({ baseURL, apiKey });
const msg = await client.messages.create({
  model: "claude-sonnet-4-5",
  max_tokens: 512,
  messages: [{ role: "user", content: "Hello from the proxy!" }],
});
const block = msg.content[0];
console.log(block?.type === "text" ? block.text : JSON.stringify(msg.content));
