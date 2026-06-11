#!/usr/bin/env python3

"""
Estimate token counts for randomly generated lorem ipsum text across OpenAI and Anthropic models.

- Uses tiktoken for OpenAI tokenization.
- Uses Anthropic Messages Count Tokens API via the official Python SDK when an API key is
  available; otherwise falls back to an approximation using cl100k_base.

Run examples:
  python scripts/token_estimation.py --sizes 256 512 1024 --trials 5 \
    --openai-models gpt-4o gpt-4o-mini gpt-4-turbo \
    --anthropic-models claude-3-5-sonnet-20240620 claude-3-5-haiku-20241022

  python scripts/token_estimation.py --sizes 2048 4096 8192 --trials 3 --csv out.csv
"""

import argparse
import os
import csv
import math
import random
import statistics
import sys
from dataclasses import dataclass
from typing import Callable, List, Optional, Tuple

try:
    import tiktoken  # type: ignore
except Exception:  # pragma: no cover
    print(
        "tiktoken is required. Please install with: pip install tiktoken",
        file=sys.stderr,
    )
    raise


# Optional Anthropic SDK import (for live token counting via API). If not available,
# we'll approximate using tiktoken's cl100k_base.
try:  # pragma: no cover - environment dependent
    import anthropic  # type: ignore
except Exception:  # pragma: no cover - environment dependent
    anthropic = None  # type: ignore


LOREM_WORDS: Tuple[str, ...] = (
    "lorem",
    "ipsum",
    "dolor",
    "sit",
    "amet",
    "consectetur",
    "adipiscing",
    "elit",
    "sed",
    "do",
    "eiusmod",
    "tempor",
    "incididunt",
    "ut",
    "labore",
    "et",
    "dolore",
    "magna",
    "aliqua",
    "ut",
    "enim",
    "ad",
    "minim",
    "veniam",
    "quis",
    "nostrud",
    "exercitation",
    "ullamco",
    "laboris",
    "nisi",
    "ut",
    "aliquip",
    "ex",
    "ea",
    "commodo",
    "consequat",
    "duis",
    "aute",
    "irure",
    "dolor",
    "in",
    "reprehenderit",
    "in",
    "voluptate",
    "velit",
    "esse",
    "cillum",
    "dolore",
    "eu",
    "fugiat",
    "nulla",
    "pariatur",
    "excepteur",
    "sint",
    "occaecat",
    "cupidatat",
    "non",
    "proident",
    "sunt",
    "in",
    "culpa",
    "qui",
    "officia",
    "deserunt",
    "mollit",
    "anim",
    "id",
    "est",
    "laborum",
)


@dataclass
class SampleStats:
    size_label: str
    model: str
    provider: str
    num_trials: int
    mean_tokens: float
    p50_tokens: float
    p95_tokens: float
    stdev_tokens: float
    mean_tokens_per_1k_chars: float


def generate_lorem_text_by_chars(target_chars: int, rng: random.Random) -> str:
    words: List[str] = []
    # Keep appending words until at or above the target character length
    while True:
        word = rng.choice(LOREM_WORDS)
        words.append(word)
        # Add a space between words except the last; compute length with spaces
        text = " ".join(words)
        if len(text) >= target_chars:
            return text[:target_chars]


def get_openai_encoder_for_model(model: str):
    # Try model-specific encoding; fallback to sensible bases
    try:
        return tiktoken.encoding_for_model(model)
    except Exception:
        # Fallback priority: o200k_base (newer), then cl100k_base
        try:
            return tiktoken.get_encoding("o200k_base")
        except Exception:
            return tiktoken.get_encoding("cl100k_base")


def count_tokens_openai(model: str, text: str) -> int:
    enc = get_openai_encoder_for_model(model)
    return len(enc.encode(text))


def get_anthropic_counter(api_key: Optional[str]) -> Callable[[str, str], int]:
    """Return a function that counts tokens for Anthropic.

    If the Anthropic SDK and an API key are available, use the live Count Tokens API.
    Otherwise, fall back to an approximation using cl100k_base.
    """
    env_key = os.environ.get("ANTHROPIC_API_KEY")
    effective_key = api_key or env_key
    if anthropic is not None and effective_key:
        client = anthropic.Anthropic(api_key=effective_key)

        approx_enc = tiktoken.get_encoding("cl100k_base")

        def _count_live(model: str, text: str) -> int:
            try:
                resp = client.messages.count_tokens(
                    model=model,
                    messages=[{"role": "user", "content": text}],
                )
                return int(resp.input_tokens)  # type: ignore[attr-defined]
            except Exception:
                return len(approx_enc.encode(text))

        return _count_live

    approx_enc = tiktoken.get_encoding("cl100k_base")

    def _count_approx(model: str, text: str) -> int:
        return len(approx_enc.encode(text))

    return _count_approx


def summarize_counts(
    size_label: str,
    provider: str,
    model: str,
    counts: List[int],
    chars: int,
) -> SampleStats:
    counts_sorted = sorted(counts)
    mean_tokens = statistics.fmean(counts)
    p50 = counts_sorted[len(counts_sorted) // 2]
    p95_index = max(0, math.ceil(0.95 * len(counts_sorted)) - 1)
    p95 = counts_sorted[p95_index]
    stdev_tokens = statistics.pstdev(counts) if len(counts) > 1 else 0.0
    mean_per_1k_chars = mean_tokens / (chars / 1000.0)
    return SampleStats(
        size_label=size_label,
        model=model,
        provider=provider,
        num_trials=len(counts),
        mean_tokens=mean_tokens,
        p50_tokens=float(p50),
        p95_tokens=float(p95),
        stdev_tokens=stdev_tokens,
        mean_tokens_per_1k_chars=mean_per_1k_chars,
    )


def run_benchmark(
    sizes: List[int],
    trials: int,
    openai_models: List[str],
    anthropic_models: List[str],
    seed: int,
    anthropic_api_key: Optional[str],
) -> List[SampleStats]:
    rng = random.Random(seed)
    results: List[SampleStats] = []

    anthropic_counter = get_anthropic_counter(anthropic_api_key)

    for chars in sizes:
        size_label = f"{chars} chars"
        # Pre-generate texts for consistent comparison across models per trial
        texts = [generate_lorem_text_by_chars(chars, rng) for _ in range(trials)]

        # OpenAI models
        for model in openai_models:
            counts = [count_tokens_openai(model, t) for t in texts]
            results.append(summarize_counts(size_label, "openai", model, counts, chars))

        # Anthropic models (tokenizer independent of model; counted for parity/reporting)
        for model in anthropic_models:
            counts = [anthropic_counter(model, t) for t in texts]
            results.append(
                summarize_counts(size_label, "anthropic", model, counts, chars)
            )

    return results


def print_results_table(results: List[SampleStats]) -> None:
    # Pretty print without extra deps
    header = (
        "Size",
        "Provider",
        "Model",
        "Trials",
        "MeanTokens",
        "P50",
        "P95",
        "Stdev",
        "MeanTok/1kChars",
    )
    row_fmt = (
        "{size:>10}  {provider:>9}  {model:<30}  {trials:>6}  "
        "{mean:>10.1f}  {p50:>6.0f}  {p95:>6.0f}  {stdev:>7.1f}  {per1k:>14.1f}"
    )
    print("\n" + " ".join(header))
    print("-" * 108)
    for r in results:
        print(
            row_fmt.format(
                size=r.size_label,
                provider=r.provider,
                model=r.model,
                trials=r.num_trials,
                mean=r.mean_tokens,
                p50=r.p50_tokens,
                p95=r.p95_tokens,
                stdev=r.stdev_tokens,
                per1k=r.mean_tokens_per_1k_chars,
            )
        )


def write_results_csv(results: List[SampleStats], csv_path: str) -> None:
    with open(csv_path, "w", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(
            [
                "size_label",
                "provider",
                "model",
                "num_trials",
                "mean_tokens",
                "p50_tokens",
                "p95_tokens",
                "stdev_tokens",
                "mean_tokens_per_1k_chars",
            ]
        )
        for r in results:
            writer.writerow(
                [
                    r.size_label,
                    r.provider,
                    r.model,
                    r.num_trials,
                    f"{r.mean_tokens:.4f}",
                    f"{r.p50_tokens:.4f}",
                    f"{r.p95_tokens:.4f}",
                    f"{r.stdev_tokens:.4f}",
                    f"{r.mean_tokens_per_1k_chars:.4f}",
                ]
            )


def parse_args(argv: Optional[List[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Estimate token counts across models.")
    parser.add_argument(
        "--sizes",
        nargs="+",
        type=int,
        default=[256, 512, 1024, 2048, 4096, 8192],
        help="Character sizes to generate and measure.",
    )
    parser.add_argument(
        "--trials",
        type=int,
        default=5,
        help="Number of random texts per size.",
    )
    parser.add_argument(
        "--seed",
        type=int,
        default=17,
        help="Random seed for reproducibility.",
    )
    parser.add_argument(
        "--openai-models",
        nargs="+",
        default=["gpt-4o", "gpt-4o-mini", "gpt-4-turbo", "gpt-5"],
        help="OpenAI models to evaluate.",
    )
    parser.add_argument(
        "--anthropic-models",
        nargs="+",
        default=[
            "claude-3-5-sonnet-20240620",
            "claude-3-5-haiku-20241022",
            "claude-sonnet-4-20250514",
        ],
        help="Anthropic models to label in output (tokenizer may be approximate).",
    )
    parser.add_argument(
        "--anthropic-api-key",
        type=str,
        default=None,
        help=(
            "Anthropic API key. If omitted, uses ANTHROPIC_API_KEY env var. "
            "If neither are set, falls back to approximation."
        ),
    )
    parser.add_argument(
        "--csv",
        type=str,
        default=None,
        help="Optional path to write results as CSV.",
    )
    return parser.parse_args(argv)


def main(argv: Optional[List[str]] = None) -> int:
    args = parse_args(argv)
    results = run_benchmark(
        sizes=args.sizes,
        trials=args.trials,
        openai_models=args.openai_models,
        anthropic_models=args.anthropic_models,
        seed=args.seed,
        anthropic_api_key=args.anthropic_api_key,
    )
    print_results_table(results)
    if args.csv:
        write_results_csv(results, args.csv)
        print(f"\nWrote CSV to {args.csv}")
    return 0


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
