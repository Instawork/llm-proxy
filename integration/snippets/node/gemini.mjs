import { GoogleGenAI } from "@google/genai";

const baseUrl = process.env.PROXY_BASE_URL;
const apiKey = process.env.PROXY_API_KEY;
if (!baseUrl || !apiKey) {
  console.error("PROXY_BASE_URL and PROXY_API_KEY required");
  process.exit(1);
}

const ai = new GoogleGenAI({ apiKey, httpOptions: { baseUrl } });
const resp = await ai.models.generateContent({
  model: "gemini-2.5-flash",
  contents: "Hello from the proxy!",
});
console.log(resp.text ?? "(empty)");
