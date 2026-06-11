process.env.ANTHROPIC_BASE_URL = process.env.PROXY_BASE_URL;
process.env.ANTHROPIC_API_KEY = process.env.PROXY_API_KEY;

if (!process.env.ANTHROPIC_BASE_URL || !process.env.ANTHROPIC_API_KEY) {
  console.error("PROXY_BASE_URL and PROXY_API_KEY required");
  process.exit(1);
}

const { default: Anthropic } = await import("@anthropic-ai/sdk");
const client = new Anthropic();
const msg = await client.messages.create({
  model: "claude-sonnet-4-5",
  max_tokens: 512,
  messages: [{ role: "user", content: "Hello from the proxy!" }],
});
const block = msg.content[0];
console.log(block?.type === "text" ? block.text : JSON.stringify(msg.content));
