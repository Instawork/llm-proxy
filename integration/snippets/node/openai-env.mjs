process.env.OPENAI_BASE_URL = process.env.PROXY_BASE_URL;
process.env.OPENAI_API_KEY = process.env.PROXY_API_KEY;

if (!process.env.OPENAI_BASE_URL || !process.env.OPENAI_API_KEY) {
  console.error("PROXY_BASE_URL and PROXY_API_KEY required");
  process.exit(1);
}

const { default: OpenAI } = await import("openai");
const client = new OpenAI();
const resp = await client.chat.completions.create({
  model: "gpt-4o",
  messages: [{ role: "user", content: "Hello from the proxy!" }],
});
console.log(resp.choices[0].message.content ?? "(empty)");
