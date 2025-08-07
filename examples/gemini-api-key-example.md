# Complete Gemini API Key Management Example

This guide walks through a complete example of creating and using a proxy API key for Google Gemini.

## Prerequisites

1. Have your actual Gemini API key ready (get one from [Google AI Studio](https://makersuite.google.com/app/apikey))
2. AWS credentials configured for DynamoDB access
3. The proxy running with API key management enabled

## Step 1: Enable API Key Management

Since you've already enabled it in `configs/dev.yml`:

```yaml
features:
  api_key_management:
    enabled: true
    table_name: "llm-proxy-api-keys-dev"
    region: "us-west-2"
```

## Step 2: Build the Tools

```bash
# Build both the proxy and key management tool
make build-all
```

## Step 3: Create a Proxy Key for Gemini

```bash
# Create a new API key for Gemini
./bin/llm-proxy-keys \
  -env=dev \
  -provider=gemini \
  -key="YOUR_ACTUAL_GEMINI_API_KEY" \
  -desc="Gemini API key for AI assistant project" \
  -cost-limit=10000 \
  -tags="project=ai-assistant,team=engineering"
```

Expected output:

```
✅ API Key Created Successfully!

Key:         iw:a3f2b8c9d4e5f6789abcdef0123456789
Provider:    gemini
Description: Gemini API key for AI assistant project
Cost Limit:  $100.00/day
Created:     2024-01-15T10:30:00Z
Tags:        map[project:ai-assistant team:engineering]

🔑 Use this key in your API requests by replacing your provider key with: iw:a3f2b8c9d4e5f6789abcdef0123456789
```

## Step 4: Start the Proxy Server

```bash
# Start the proxy in development mode
LOG_LEVEL=debug ENVIRONMENT=dev ./bin/llm-proxy
```

You should see:

```
INFO [10:35:00] 🔑 API Key Store: API key management is enabled in config
INFO [10:35:00] 🔑 API Key Store: Initializing API key store; table_name=llm-proxy-api-keys-dev, region=us-west-2
INFO [10:35:01] 🔑 API Key Store: Successfully initialized API key store
INFO [10:35:01] Starting LLM Proxy server on :9002
```

## Step 5: Use the Proxy Key with Gemini

### Example 1: Using Query Parameter (Most Common)

```bash
# Using the proxy key in the URL query parameter
curl -X POST "http://localhost:9002/gemini/v1beta/models/gemini-pro:generateContent?key=iw:a3f2b8c9d4e5f6789abcdef0123456789" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{
      "parts": [{
        "text": "Explain how API key management improves security in 3 bullet points"
      }]
    }]
  }'
```

### Example 2: Using Header Authentication

```bash
# Using the proxy key in the x-goog-api-key header
curl -X POST "http://localhost:9002/gemini/v1beta/models/gemini-flash-2.5:generateContent" \
  -H "x-goog-api-key: iw:a3f2b8c9d4e5f6789abcdef0123456789" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{
      "parts": [{
        "text": "What is the capital of France?"
      }]
    }]
  }'
```

### Example 3: Streaming Response

```bash
# Enable streaming with alt=sse parameter
curl -X POST "http://localhost:9002/gemini/v1beta/models/gemini-pro:streamGenerateContent?key=iw:a3f2b8c9d4e5f6789abcdef0123456789&alt=sse" \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -d '{
    "contents": [{
      "parts": [{
        "text": "Write a haiku about API keys"
      }]
    }]
  }'
```

## Step 6: Python Example

```python
import requests
import json

# Your proxy key (not the actual Gemini key!)
PROXY_KEY = "iw:a3f2b8c9d4e5f6789abcdef0123456789"
PROXY_URL = "http://localhost:9002"

def generate_content(prompt):
    """Generate content using Gemini through the proxy"""
    
    url = f"{PROXY_URL}/gemini/v1beta/models/gemini-pro:generateContent"
    
    headers = {
        "Content-Type": "application/json",
        "x-goog-api-key": PROXY_KEY  # Using the proxy key
    }
    
    data = {
        "contents": [{
            "parts": [{
                "text": prompt
            }]
        }]
    }
    
    response = requests.post(url, headers=headers, json=data)
    
    if response.status_code == 200:
        result = response.json()
        return result['candidates'][0]['content']['parts'][0]['text']
    else:
        print(f"Error: {response.status_code}")
        print(response.text)
        return None

# Example usage
if __name__ == "__main__":
    response = generate_content("What are the benefits of using an API proxy?")
    print(response)
```

## Step 7: Node.js Example

```javascript
const axios = require('axios');

// Your proxy key (not the actual Gemini key!)
const PROXY_KEY = 'iw:a3f2b8c9d4e5f6789abcdef0123456789';
const PROXY_URL = 'http://localhost:9002';

async function generateContent(prompt) {
    const url = `${PROXY_URL}/gemini/v1beta/models/gemini-pro:generateContent`;
    
    const data = {
        contents: [{
            parts: [{
                text: prompt
            }]
        }]
    };
    
    try {
        // Method 1: Using header
        const response = await axios.post(url, data, {
            headers: {
                'Content-Type': 'application/json',
                'x-goog-api-key': PROXY_KEY
            }
        });
        
        // Method 2: Using query parameter
        // const response = await axios.post(
        //     `${url}?key=${PROXY_KEY}`,
        //     data,
        //     { headers: { 'Content-Type': 'application/json' } }
        // );
        
        return response.data.candidates[0].content.parts[0].text;
    } catch (error) {
        console.error('Error:', error.response?.data || error.message);
        return null;
    }
}

// Example usage
(async () => {
    const response = await generateContent('Explain quantum computing in simple terms');
    console.log(response);
})();
```

## Step 8: Managing Your Keys

### List all Gemini keys

```bash
./bin/llm-proxy-keys -env=dev -list -provider=gemini
```

Output:

```
KEY                                      PROVIDER  DESCRIPTION                               COST LIMIT  ENABLED  CREATED
---                                      --------  -----------                               ----------  -------  -------
iw:a3f2b8c9d4e5f6789abcdef0123456789   gemini    Gemini API key for AI assistant project  $100.00/day  true     2024-01-15
```

### View key details

```bash
./bin/llm-proxy-keys -env=dev -show=iw:a3f2b8c9d4e5f6789abcdef0123456789
```

### Disable a key temporarily

```bash
./bin/llm-proxy-keys -env=dev -disable=iw:a3f2b8c9d4e5f6789abcdef0123456789
```

### Re-enable a key

```bash
./bin/llm-proxy-keys -env=dev -enable=iw:a3f2b8c9d4e5f6789abcdef0123456789
```

### Delete a key permanently

```bash
./bin/llm-proxy-keys -env=dev -delete=iw:a3f2b8c9d4e5f6789abcdef0123456789
```

## Step 9: Testing Different Gemini Models

The proxy key works with all Gemini models:

### Gemini Pro

```bash
curl -X POST "http://localhost:9002/gemini/v1beta/models/gemini-pro:generateContent?key=iw:a3f2b8c9d4e5f6789abcdef0123456789" \
  -H "Content-Type: application/json" \
  -d '{"contents": [{"parts": [{"text": "Hello"}]}]}'
```

### Gemini Pro Vision (with image)

```bash
# First, convert image to base64
IMAGE_BASE64=$(base64 -i image.jpg)

curl -X POST "http://localhost:9002/gemini/v1beta/models/gemini-pro-vision:generateContent?key=iw:a3f2b8c9d4e5f6789abcdef0123456789" \
  -H "Content-Type: application/json" \
  -d "{
    \"contents\": [{
      \"parts\": [
        {\"text\": \"What's in this image?\"},
        {\"inline_data\": {
          \"mime_type\": \"image/jpeg\",
          \"data\": \"$IMAGE_BASE64\"
        }}
      ]
    }]
  }"
```

## Debugging Tips

### 1. Check Proxy Logs

When a request uses a proxy key, you'll see:

```
🔑 Gemini: Translated API key from iw: format
```

### 2. Test Key Validation

Try using an invalid key:

```bash
curl -X POST "http://localhost:9002/gemini/v1beta/models/gemini-pro:generateContent?key=iw:invalid" \
  -H "Content-Type: application/json" \
  -d '{"contents": [{"parts": [{"text": "Test"}]}]}'
```

Expected response (401 Unauthorized):

```json
{
  "error": "Invalid API key: API key not found"
}
```

### 3. Verify DynamoDB Table

Check if the table was created:

```bash
aws dynamodb describe-table \
  --table-name llm-proxy-api-keys-dev \
  --region us-west-2
```

List items in the table:

```bash
aws dynamodb scan \
  --table-name llm-proxy-api-keys-dev \
  --region us-west-2
```

## Security Best Practices

1. **Never expose actual Gemini keys**: Only share the `iw:` prefixed proxy keys with your team
2. **Use HTTPS in production**: Always use HTTPS when deploying the proxy
3. **Rotate keys regularly**: Create new proxy keys and disable old ones periodically
4. **Monitor usage**: Check logs for unusual activity
5. **Set appropriate cost limits**: Use the `-cost-limit` flag to prevent unexpected charges

## Troubleshooting

### Error: "API key validation failed"

- Check if the key exists: `./bin/llm-proxy-keys -env=dev -show=YOUR_KEY`
- Verify the key is enabled
- Ensure the key is for the correct provider (gemini)

### Error: "Failed to create API key store"

- Verify AWS credentials are configured
- Check the AWS region is correct
- Ensure you have DynamoDB permissions

### Error: "Invalid API key format"

- Make sure the key starts with `iw:`
- Check for typos in the key

## Integration with Google AI SDK

If you're using the Google Generative AI SDK, you can configure it to use the proxy:

```python
import google.generativeai as genai

# Configure to use proxy
genai.configure(
    api_key="iw:a3f2b8c9d4e5f6789abcdef0123456789",
    transport='rest',
    client_options={
        'api_endpoint': 'http://localhost:9002/gemini'
    }
)

model = genai.GenerativeModel('gemini-pro')
response = model.generate_content("Write a story about a magic proxy server")
print(response.text)
```

## Complete End-to-End Test Script

```bash
#!/bin/bash

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${BLUE}Starting Gemini API Key Management Test${NC}"

# Step 1: Create a test key
echo -e "\n${BLUE}1. Creating test API key...${NC}"
KEY_OUTPUT=$(./bin/llm-proxy-keys \
  -env=dev \
  -provider=gemini \
  -key="$GEMINI_API_KEY" \
  -desc="Test key for demo" \
  -cost-limit=1000)

# Extract the proxy key from output
PROXY_KEY=$(echo "$KEY_OUTPUT" | grep "Key:" | awk '{print $2}')
echo -e "${GREEN}Created key: $PROXY_KEY${NC}"

# Step 2: Test the key
echo -e "\n${BLUE}2. Testing API call with proxy key...${NC}"
RESPONSE=$(curl -s -X POST \
  "http://localhost:9002/gemini/v1beta/models/gemini-pro:generateContent?key=$PROXY_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{
      "parts": [{"text": "Say hello in JSON format"}]
    }]
  }')

echo "Response: $RESPONSE" | jq .

# Step 3: List keys
echo -e "\n${BLUE}3. Listing all Gemini keys...${NC}"
./bin/llm-proxy-keys -env=dev -list -provider=gemini

# Step 4: Disable the key
echo -e "\n${BLUE}4. Disabling the key...${NC}"
./bin/llm-proxy-keys -env=dev -disable=$PROXY_KEY

# Step 5: Try using disabled key (should fail)
echo -e "\n${BLUE}5. Testing disabled key (should fail)...${NC}"
curl -s -X POST \
  "http://localhost:9002/gemini/v1beta/models/gemini-pro:generateContent?key=$PROXY_KEY" \
  -H "Content-Type: application/json" \
  -d '{"contents": [{"parts": [{"text": "Test"}]}]}'

# Step 6: Clean up
echo -e "\n${BLUE}6. Cleaning up - deleting test key...${NC}"
./bin/llm-proxy-keys -env=dev -delete=$PROXY_KEY

echo -e "\n${GREEN}Test completed!${NC}"
```

This example provides a complete walkthrough of using the API key management system with Gemini, including practical code examples and troubleshooting tips.
