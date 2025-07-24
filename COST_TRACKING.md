# Cost Tracking Guide

Your LLM proxy now tracks the cost of each request and stores it in a readable file for analysis. This guide explains how to use the cost tracking feature.

## Overview

The cost tracking system:

- ✅ Captures every request with model, token usage, and calculated costs
- ✅ Uses pricing data from your `config.yml`
- ✅ Stores data in JSON Lines format for easy reading
- ✅ Provides analysis tools for cost reporting

## Configuration

### Enable Cost Tracking

Cost tracking is controlled by the `features.cost_tracking.enabled` setting in your `config.yml`:

```yaml
features:
  cost_tracking:
    enabled: true  # Set to false to disable
```

### Set Output File Location

By default, cost data is written to `logs/cost-tracking.jsonl`. You can customize this with an environment variable:

```bash
export COST_TRACKING_FILE="/path/to/your/cost-data.jsonl"
```

### Pricing Configuration

Pricing is already configured in your `config.yml` for various models:

```yaml
providers:
  openai:
    models:
      "gpt-4o":
        pricing:
          input: 15.00   # Cost per 1M input tokens in USD
          output: 30.00  # Cost per 1M output tokens in USD
```

## Data Format

Each line in the cost tracking file is a JSON record with this structure:

```json
{
  "timestamp": "2024-12-19T18:59:02.123456Z",
  "request_id": "chatcmpl-abc123",
  "user_id": "token:sk-abc123",
  "ip_address": "192.168.1.1",
  "provider": "openai",
  "model": "gpt-4o",
  "endpoint": "/openai/v1/chat/completions",
  "is_streaming": false,
  "input_tokens": 50,
  "output_tokens": 25,
  "total_tokens": 75,
  "input_cost": 0.00075,
  "output_cost": 0.00075,
  "total_cost": 0.0015,
  "finish_reason": "stop"
}
```

## Analysis Tools

### Quick Summary Report

```bash
python scripts/analyze_costs.py
```

This shows:

- Total cost and requests for the last 24 hours
- Breakdown by provider and model
- Average costs per request and per token

Example output:

```
============================================================
LLM PROXY COST ANALYSIS SUMMARY
============================================================
Time Period: 2024-12-19T10:30:00 to 2024-12-19T18:30:00
Total Cost: $12.345678
Total Requests: 147
Total Tokens: 45,230
Average Cost per Request: $0.084010
Average Cost per 1K Tokens: $0.273005

COST BY PROVIDER:
----------------------------------------
openai       | $8.234567 |    89 req |   28,150 tokens
anthropic    | $3.456789 |    42 req |   12,890 tokens
gemini       | $0.654322 |    16 req |    4,190 tokens

TOP MODELS BY COST:
--------------------------------------------------
openai/gpt-4o            | $5.123456 |    34 req |   15,230 tokens
anthropic/claude-3-opus  | $2.876543 |    18 req |    7,450 tokens
openai/gpt-4o-mini       | $1.456789 |    55 req |   12,920 tokens
```

### Detailed Request Log

```bash
python scripts/analyze_costs.py --format detailed
```

Shows individual requests with timestamps, models, costs, and users.

### Export to CSV

```bash
python scripts/analyze_costs.py --format csv
```

Creates a CSV file that you can open in Excel or other tools for further analysis.

### Hourly Breakdown

```bash
python scripts/analyze_costs.py --format hourly
```

Shows cost patterns by hour to identify usage peaks.

### Custom Time Ranges

```bash
# Last 7 days
python scripts/analyze_costs.py --since 168

# Last week
python scripts/analyze_costs.py --since 168 --format summary
```

## Manual Analysis

The JSON Lines format is easy to work with using standard tools:

### View recent requests with jq

```bash
tail -10 logs/cost-tracking.jsonl | jq '.'
```

### Find expensive requests

```bash
cat logs/cost-tracking.jsonl | jq 'select(.total_cost > 0.01)'
```

### Sum costs by model

```bash
cat logs/cost-tracking.jsonl | jq -r '.provider + "/" + .model' | sort | uniq -c
```

### Import into your own tools

```python
import json

with open('logs/cost-tracking.jsonl', 'r') as f:
    for line in f:
        record = json.loads(line)
        print(f"Cost: ${record['total_cost']:.6f} for {record['model']}")
```

## Health Check

The `/health` endpoint now includes cost tracking information:

```bash
curl http://localhost:9002/health
```

Returns:

```json
{
  "status": "healthy",
  "features": {
    "cost_tracking": true
  },
  "cost_stats_24h": {
    "total_cost": 12.345,
    "provider_costs": {
      "openai": 8.234,
      "anthropic": 3.456
    }
  }
}
```

## Troubleshooting

### No cost data being generated

1. Check that `features.cost_tracking.enabled: true` in `config.yml`
2. Verify pricing is configured for your models
3. Make requests to ensure the system is processing them
4. Check logs for cost tracking messages like `💵 Cost Tracking:`

### Missing pricing for models

If you see "Cost: Not calculated (no pricing configured)", add pricing to your `config.yml`:

```yaml
providers:
  openai:
    models:
      "your-model-name":
        pricing:
          input: 1.50   # Cost per 1M input tokens
          output: 2.00  # Cost per 1M output tokens
```

### File permissions

Ensure the proxy can write to the logs directory:

```bash
mkdir -p logs
chmod 755 logs
```

## Integration Examples

### Daily cost reports via cron

```bash
# Add to crontab
0 9 * * * cd /path/to/llm-proxy && python scripts/analyze_costs.py --since 24 --format summary | mail -s "Daily LLM Costs" admin@company.com
```

### Monitoring with Prometheus/Grafana

You can parse the JSONL file and expose metrics for monitoring systems.

### Billing integration

Use the CSV export or JSON data to integrate with your billing system.

---

The cost tracking system gives you complete visibility into your LLM usage costs, helping you optimize spending and understand usage patterns.
