import OpenAI from "openai";

const baseURL = process.env.PROXY_BASE_URL;
const apiKey = process.env.PROXY_API_KEY;
if (!baseURL || !apiKey) {
  console.error("PROXY_BASE_URL and PROXY_API_KEY required");
  process.exit(1);
}

const client = new OpenAI({ baseURL, apiKey });
const resp = await client.chat.completions.create({
  model: "gpt-4o",
  messages: [{ role: "user", content: "Hello from the proxy!" }],
});
console.log(resp.choices[0].message.content ?? "(empty)");
