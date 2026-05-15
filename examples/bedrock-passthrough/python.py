"""Route AWS Bedrock Converse requests through llm-proxy via SigV4 passthrough.

This example shows the boto3 ``before-send`` hook recipe used to make
``langchain-aws.ChatBedrockConverse`` (or any other boto3 ``bedrock-runtime``
client) connect to ``llm-proxy`` instead of AWS directly — while still signing
the request against the real Bedrock host so AWS's IAM validation passes.

Why this is needed
------------------

Unlike OpenAI / Anthropic / Gemini, Bedrock does not authenticate with a static
API key — every request is SigV4-signed against the canonical Bedrock host
(`bedrock-runtime.<region>.amazonaws.com`). The proxy is a transparent
**byte-for-byte passthrough**:

* it strips its own ``/bedrock`` URL prefix so the upstream sees the path the
  client signed (`/model/{modelId}/converse[-stream]`);
* it preserves ``Authorization``, ``X-Amz-Date``, ``X-Amz-Content-Sha256``,
  and the request body unchanged;
* it never mutates any header that is part of the SigV4 canonical request.

Because mutating any signed value (including the destination URL host) would
invalidate the signature, the trick is to **sign first, then rewrite the URL**.
botocore's ``before-send`` event fires after the signer has finished and is
the canonical hook for this pattern.

Running this example
--------------------

1. Start the proxy with Bedrock enabled — flip ``providers.bedrock.enabled: true``
   in ``configs/<env>.yml`` (or override locally), then::

       make run

2. Make sure you have AWS credentials in the standard chain (env vars, profile,
   IAM role / IRSA, etc.) and that those credentials have ``bedrock:Converse``
   in the region you target.

3. Install the dependencies::

       pip install boto3 langchain-aws

4. Run::

       python examples/bedrock-passthrough/python.py
"""

from __future__ import annotations

import os

import boto3
from langchain_aws import ChatBedrockConverse

PROXY_URL = os.environ.get("LLM_PROXY_URL", "http://localhost:9002")
REGION = os.environ.get("AWS_REGION", "us-west-2")
MODEL_ID = os.environ.get(
    "BEDROCK_MODEL_ID",
    "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
)


def make_bedrock_session(proxy_url: str = PROXY_URL, region: str = REGION) -> boto3.Session:
    """Return a boto3 Session whose ``bedrock-runtime`` traffic is signed
    against the real AWS host but actually connects to ``llm-proxy``.

    The ``before-send.bedrock-runtime`` event fires *after* the SigV4 signer
    has finished, so ``Authorization`` / ``X-Amz-Content-Sha256`` have already
    been computed against the canonical Bedrock URL by the time we rewrite the
    destination. The proxy strips ``/bedrock`` and forwards verbatim, so AWS
    receives a request with a valid signature.
    """
    session = boto3.Session(region_name=region)
    aws_host = f"https://bedrock-runtime.{region}.amazonaws.com"
    proxy_root = proxy_url.rstrip("/") + "/bedrock"

    def _route_to_proxy(request, **_kwargs) -> None:  # botocore signature
        if request.url.startswith(aws_host):
            request.url = request.url.replace(aws_host, proxy_root, 1)

    session.events.register("before-send.bedrock-runtime", _route_to_proxy)
    return session


def main() -> None:
    session = make_bedrock_session()
    llm = ChatBedrockConverse(
        model_id=MODEL_ID,
        client=session.client("bedrock-runtime"),
        # Use whatever sampling / max_tokens / etc you'd normally pass.
        temperature=0.2,
        max_tokens=128,
    )
    answer = llm.invoke("In one sentence, what is the LLM proxy?")
    print(f"Model: {MODEL_ID}")
    print(f"Region: {REGION}")
    print(f"Proxy: {PROXY_URL}")
    print()
    print("Response:")
    print(answer.content)


if __name__ == "__main__":
    main()
